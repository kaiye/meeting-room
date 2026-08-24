"""OpenAI-compatible transcription gateway: FunASR pipeline + optional MOSS backend.

Implements the ``gpt-4o-transcribe-diarize`` protocol. The ``model`` form field
routes between two engines:

- ``gpt-4o-transcribe-diarize`` (default): local FunASR pipeline
  (Paraformer-large ASR + FSMN-VAD + CT-punc + CAM++ diarization)
- ``moss-transcribe-diarize``: proxies to a MOSS-Transcribe-Diarize server
  (``--moss-url``), then replaces its S01/S02 labels via CAM++ voiceprint
  matching against ``known_speaker_references`` — emulating known-speaker
  support on a backend that lacks it.

Common protocol surface:

- ``POST /v1/audio/transcriptions`` (multipart form)
- ``response_format``: ``json`` | ``text`` | ``diarized_json`` | ``verbose_json``
- ``known_speaker_names[]`` + ``known_speaker_references[]``: up to 4 enrolled
  speakers per request (data URLs, raw base64, or file parts); diarized clusters
  are voiceprint-matched via CAM++ and labeled with the given names, otherwise
  ``A``/``B``/``C``... in order of first appearance.
- ``stream=true``: SSE with ``transcript.text.delta`` / ``transcript.text.segment``
  / ``transcript.text.done`` events. Note: the offline pipeline finishes inference
  before events are emitted (protocol-level streaming, not incremental ASR — use
  the websocket 2pass server for true live captions).
- ``chunking_strategy`` accepted and ignored (FSMN-VAD segments natively);
  ``language`` / ``prompt`` / ``temperature`` accepted and ignored (Paraformer
  is a zh/en non-autoregressive model with no decoding temperature).

Run:
  .venv/bin/python funasr_openai_server.py --host 127.0.0.1 --port 8100

Optional: ``--speaker-db speaker_db.json`` (enroll_speaker.py format) matches
every request against pre-enrolled voiceprints even without per-request
``known_speaker_references``.
"""

from __future__ import annotations

import argparse
import base64
import binascii
import hashlib
import json
import subprocess
import tempfile
import threading
from pathlib import Path
from typing import Any, Iterator

import numpy as np
import soundfile as sf
from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import JSONResponse, PlainTextResponse, StreamingResponse
from starlette.concurrency import run_in_threadpool
from starlette.datastructures import UploadFile

RESPONSE_FORMATS = {"json", "text", "diarized_json", "verbose_json"}
REF_CACHE_MAX = 128  # cached reference embeddings, keyed by audio content hash
CLUSTER_SAMPLE_SECONDS = 15.0  # audio per cluster fed to the voiceprint model
SENTENCE_PUNCT = "，。？！；,.?!;"
MIN_SUB_SECONDS = 1.0    # shorter clips give unstable CAM++ embeddings
SAME_SPEAKER_THRES = 0.45  # measured: same speaker ~0.65-0.75, different ~0.14-0.26
SAMPLE_RATE = 16000

_infer_lock = threading.Lock()


# --------------------------------------------------------------------------- audio

def ffmpeg_to_16k(src: Path, dst: Path) -> None:
    proc = subprocess.run(
        ["ffmpeg", "-y", "-loglevel", "error", "-i", str(src),
         "-ac", "1", "-ar", str(SAMPLE_RATE), str(dst)],
        capture_output=True, text=True,
    )
    if proc.returncode != 0:
        raise HTTPException(status_code=400, detail=f"Could not decode audio: {proc.stderr.strip()[:300]}")


def _reference_bytes(value: Any, index: int) -> bytes:
    """Accept a data URL, raw base64 string, or uploaded file part; return audio bytes."""
    if isinstance(value, UploadFile):
        return value.file.read()
    if isinstance(value, str):
        if value.startswith("data:") and "," not in value:
            # curl's -F treats ';' in "data:audio/wav;base64,..." as a content-type
            # separator and silently truncates the value to "data:audio/wav".
            raise HTTPException(
                status_code=400,
                detail=f"known_speaker_references[{index}] is a truncated data URL "
                "(no comma found). If you are using curl, pass this field with "
                "--form-string instead of -F.",
            )
        payload = value.split(",", 1)[1] if value.startswith("data:") else value
        try:
            return base64.b64decode(payload, validate=True)
        except (binascii.Error, ValueError) as exc:
            raise HTTPException(
                status_code=400,
                detail=f"known_speaker_references[{index}] is not a valid data URL, "
                "base64 string, or file part.",
            ) from exc
    raise HTTPException(
        status_code=400,
        detail=f"known_speaker_references[{index}] has unsupported type.",
    )


# --------------------------------------------------------------------------- text

def _is_cjk(ch: str) -> bool:
    code = ord(ch)
    return (
        0x4E00 <= code <= 0x9FFF
        or 0x3400 <= code <= 0x4DBF
        or 0x3000 <= code <= 0x303F
        or 0xFF00 <= code <= 0xFFEF
    )


def join_segment_texts(texts: list[str]) -> str:
    out: list[str] = []
    for text in texts:
        text = text.strip()
        if not text:
            continue
        if out and not (_is_cjk(out[-1][-1]) or _is_cjk(text[0])):
            out.append(" ")
        out.append(text)
    return "".join(out)


# --------------------------------------------------------------------------- engine

class Engine:
    """Holds the ASR pipeline and the voiceprint model."""

    def __init__(
        self,
        speaker_db_path: Path | None,
        sv_threshold: float,
        max_known_speakers: int = 4,
        speech_noise_thres: float = 0.2,
        max_end_silence_time: int = 1200,
    ) -> None:
        from funasr import AutoModel

        self.sv_threshold = sv_threshold
        self.speaker_db_path = speaker_db_path
        self.max_known_speakers = max_known_speakers
        self._ref_cache: dict[str, np.ndarray] = {}
        self._nano = None
        self._asr = None
        self._vad_kwargs = {
            "max_single_segment_time": 30000,
            "speech_noise_thres": speech_noise_thres,
            "max_end_silence_time": max_end_silence_time,
        }
        self.sv = AutoModel(
            model="iic/speech_campplus_sv_zh-cn_16k-common",
            ngpu=0, device="cpu",
            disable_update=True, disable_pbar=True, disable_log=True,
        )

    def embed(self, wav_path: Path) -> np.ndarray:
        out = self.sv.generate(input=str(wav_path), embedding=True)[0]
        vec = out["spk_embedding"][0].cpu().numpy().astype(np.float64)
        return vec / np.linalg.norm(vec)

    def embed_reference(self, raw: bytes, index: int, tmpdir: Path) -> np.ndarray:
        """Embed a reference clip, caching by content hash (chunked clients resend
        the same references on every request)."""
        key = hashlib.sha256(raw).hexdigest()
        cached = self._ref_cache.get(key)
        if cached is not None:
            return cached
        raw_path = tmpdir / f"ref_{index}_raw"
        raw_path.write_bytes(raw)
        wav_path = tmpdir / f"ref_{index}.wav"
        ffmpeg_to_16k(raw_path, wav_path)
        vec = self.embed(wav_path)
        if len(self._ref_cache) >= REF_CACHE_MAX:
            self._ref_cache.pop(next(iter(self._ref_cache)))
        self._ref_cache[key] = vec
        return vec

    def enrolled_speakers(self) -> dict[str, np.ndarray]:
        if not self.speaker_db_path or not self.speaker_db_path.exists():
            return {}
        db = json.loads(self.speaker_db_path.read_text(encoding="utf-8"))
        out = {}
        for name, emb in db.items():
            vec = np.asarray(emb, dtype=np.float64)
            out[name] = vec / np.linalg.norm(vec)
        return out

    def _asr_model(self):
        """Paraformer pipeline, loaded on first use so a gateway that only serves
        other engines does not pay its ~1.6GB."""
        if self._asr is None:
            from funasr import AutoModel

            print("[paraformer] loading (first request)...", flush=True)
            self._asr = AutoModel(
                model="paraformer-zh",
                vad_model="fsmn-vad",
                punc_model="ct-punc",
                spk_model="cam++",
                # FSMN-VAD defaults are tuned for close-mic audio and silently drop
                # quiet far-field speech in meeting rooms — measured ~25% content
                # loss on real recordings. These thresholds keep that speech.
                vad_kwargs=self._vad_kwargs,
                disable_update=True, disable_pbar=True, disable_log=True,
            )
            print("[paraformer] ready", flush=True)
        return self._asr

    def _nano_model(self):
        """Fun-ASR-Nano-2512: SenseVoice encoder + Qwen3-0.6B decoder, ~800M.
        Loaded on first use — it costs ~2GB resident and most requests never need it."""
        if self._nano is None:
            from funasr import AutoModel

            print("[nano] loading Fun-ASR-Nano-2512 (first request)...", flush=True)
            # No punc_model here on purpose: Fun-ASR-Nano emits its own timestamps,
            # and FunASR skips the external punctuation model whenever the ASR
            # result already carries them — loading it would cost ~700MB unused.
            self._nano = AutoModel(
                model="FunAudioLLM/Fun-ASR-Nano-2512",
                vad_model="fsmn-vad",
                spk_model="cam++",
                vad_kwargs=self._vad_kwargs,
                device="cpu", ngpu=0,
                disable_update=True, disable_pbar=True, disable_log=True,
            )
            print("[nano] ready", flush=True)
        return self._nano

    def transcribe_nano(self, wav_path: Path, hotwords: list[str] | None = None) -> list[dict[str, Any]]:
        model = self._nano_model()
        extra = {"hotword": " ".join(hotwords)} if hotwords else {}
        with _infer_lock:
            res = model.generate(
                input=str(wav_path), batch_size_s=300, sentence_timestamp=True, **extra,
            )
        return self._to_sentences(res[0].get("sentence_info", []) if res else [])

    @staticmethod
    def _to_sentences(raw: list[dict[str, Any]]) -> list[dict[str, Any]]:
        out = []
        for s in raw:
            # Paraformer uses "text"; Fun-ASR-Nano uses "sentence".
            text = (s.get("text") or s.get("sentence") or "").strip()
            if not text:
                continue
            out.append({
                "start": round(s.get("start", 0) / 1000, 2),
                "end": round(s.get("end", 0) / 1000, 2),
                "cluster": int(s.get("spk", -1)),
                "text": text,
            })
        return out

    def transcribe(self, wav_path: Path, hotwords: list[str] | None = None) -> list[dict[str, Any]]:
        # paraformer-zh resolves to SeACo-Paraformer, which biases decoding
        # toward a space-separated hotword string.
        extra = {"hotword": " ".join(hotwords)} if hotwords else {}
        model = self._asr_model()
        with _infer_lock:
            res = model.generate(
                input=str(wav_path), batch_size_s=300, sentence_timestamp=True, **extra,
            )
        return self._to_sentences(res[0].get("sentence_info", []) if res else [])

    def refine_segments(
        self, sentences: list[dict[str, Any]], wav_path: Path, tmpdir: Path
    ) -> list[dict[str, Any]]:
        """Split VAD-sized segments at punctuation and re-derive speakers per piece.

        Fun-ASR-Nano carries its own timestamps, so FunASR skips the punctuation
        model and can only cluster speakers per VAD segment — a turn change with
        no silence gap gets swallowed. Splitting on punctuation and re-running the
        voiceprint model recovers those turns.
        """
        audio, sr = sf.read(wav_path, dtype="float32")
        pieces: list[dict[str, Any]] = []
        for seg in sentences:
            for sub in _split_on_punctuation(seg):
                pieces.append(sub)

        centroids: list[tuple[np.ndarray, int]] = []
        for i, p in enumerate(pieces):
            lo, hi = int(p["start"] * sr), min(int(p["end"] * sr), len(audio))
            if hi - lo < int(MIN_SUB_SECONDS * sr):
                # too short to identify: inherit the previous piece's speaker
                p["cluster"] = pieces[i - 1]["cluster"] if i else 0
                continue
            clip = tmpdir / f"sub_{i}.wav"
            sf.write(clip, audio[lo:hi], sr)
            vec = self.embed(clip)
            clip.unlink(missing_ok=True)

            best, best_score = -1, -1.0
            for idx, (centroid, _) in enumerate(centroids):
                score = float(np.dot(vec, centroid))
                if score > best_score:
                    best, best_score = idx, score
            if best_score >= SAME_SPEAKER_THRES:
                centroid, n = centroids[best]
                merged = centroid * n + vec
                centroids[best] = (merged / np.linalg.norm(merged), n + 1)
                p["cluster"] = best
            else:
                centroids.append((vec, 1))
                p["cluster"] = len(centroids) - 1
        return pieces

    def label_speakers(
        self,
        sentences: list[dict[str, Any]],
        wav_path: Path,
        known: dict[str, np.ndarray],
        tmpdir: Path,
    ) -> None:
        """Map cluster ids to names (voiceprint match) or A/B/C (first appearance)."""
        letters: dict[int, str] = {}
        for s in sentences:
            if s["cluster"] not in letters:
                letters[s["cluster"]] = chr(ord("A") + len(letters) % 26)

        matched: dict[int, str] = {}
        if known:
            audio, sr = sf.read(wav_path, dtype="float32")
            for cluster in letters:
                clip = self._cluster_clip(sentences, cluster, audio, sr)
                if clip is None:
                    continue
                clip_path = tmpdir / f"cluster_{cluster}.wav"
                sf.write(clip_path, clip, sr)
                emb = self.embed(clip_path)
                scores = {name: float(np.dot(emb, ref)) for name, ref in known.items()}
                best = max(scores, key=scores.get)
                if scores[best] > self.sv_threshold:
                    matched[cluster] = best

        for s in sentences:
            s["speaker"] = matched.get(s["cluster"], letters[s["cluster"]])

    @staticmethod
    def _cluster_clip(
        sentences: list[dict[str, Any]], cluster: Any, audio: np.ndarray, sr: int
    ) -> np.ndarray | None:
        # Spans where any OTHER speaker talks: subtracted from candidates so
        # overlapping speech does not pollute the voiceprint embedding.
        others = [
            (s["start"], s["end"]) for s in sentences if s["cluster"] != cluster
        ]

        def clean_subspans(lo: float, hi: float) -> list[tuple[float, float]]:
            spans = [(lo, hi)]
            for a, b in others:
                nxt: list[tuple[float, float]] = []
                for x, y in spans:
                    if b <= x or a >= y:
                        nxt.append((x, y))
                        continue
                    if a > x:
                        nxt.append((x, a))
                    if b < y:
                        nxt.append((b, y))
                spans = nxt
            return spans

        candidates: list[tuple[float, float]] = []
        for s in sentences:
            if s["cluster"] == cluster:
                candidates.extend(clean_subspans(s["start"], s["end"]))
        candidates = [c for c in candidates if c[1] - c[0] >= 0.5]
        candidates.sort(key=lambda c: c[1] - c[0], reverse=True)

        parts, total = [], 0.0
        for lo_s, hi_s in candidates:
            lo, hi = int(lo_s * sr), min(int(hi_s * sr), len(audio))
            if hi <= lo:
                continue
            parts.append(audio[lo:hi])
            total += (hi - lo) / sr
            if total >= CLUSTER_SAMPLE_SECONDS:
                break
        if not parts:
            return None
        return np.concatenate(parts)


def _split_on_punctuation(seg: dict[str, Any]) -> list[dict[str, Any]]:
    """Cut one segment into sentence-sized pieces at punctuation.

    Timing is interpolated by character count — Fun-ASR-Nano's own word
    timestamps are not exposed per sentence, and Chinese speech rate is even
    enough that proportional allocation lands within a few hundred ms.
    """
    text = seg["text"]
    parts, buf = [], []
    for ch in text:
        buf.append(ch)
        if ch in SENTENCE_PUNCT:
            parts.append("".join(buf))
            buf = []
    if buf:
        parts.append("".join(buf))
    parts = [p for p in parts if p.strip()]

    span = seg["end"] - seg["start"]
    if len(parts) <= 1 or span < 2 * MIN_SUB_SECONDS:
        return [seg]

    total = sum(len(p) for p in parts)
    out: list[dict[str, Any]] = []
    cursor = seg["start"]
    for p in parts:
        dur = span * len(p) / total
        piece = {
            "start": round(cursor, 2),
            "end": round(min(cursor + dur, seg["end"]), 2),
            "cluster": seg["cluster"],
            "text": p,
        }
        # fold pieces too short to embed into the previous one
        if out and piece["end"] - piece["start"] < MIN_SUB_SECONDS:
            out[-1]["end"] = piece["end"]
            out[-1]["text"] += piece["text"]
        else:
            out.append(piece)
        cursor += dur
    return out


# --------------------------------------------------------------------------- moss proxy

MOSS_DEFAULT_PROMPT = (
    "请将音频转写为文本，每一段需以起始时间戳和说话人编号"
    "（[S01]、[S02]、[S03]…）开头，正文为对应的语音内容，"
    "并在段末标注结束时间戳，以清晰标明该段语音范围。"
)


def moss_transcribe(
    src_path: Path,
    moss_url: str,
    language: str | None,
    hotwords: list[str] | None = None,
    prompt: str | None = None,
) -> list[dict[str, Any]]:
    """Forward audio to a MOSS server; return sentences with S01/S02 cluster keys."""
    import requests

    data = {"model": "moss-transcribe-diarize", "response_format": "diarized_json"}
    if language:
        data["language"] = language
    # Hotwords ride on MOSS's prompt channel. Always keep the diarization format
    # instruction: dropping it changes the output shape and breaks parsing.
    if prompt:
        data["prompt"] = prompt
    elif hotwords:
        data["prompt"] = f"{MOSS_DEFAULT_PROMPT}热词提示：{', '.join(hotwords)}"
    try:
        with src_path.open("rb") as f:
            resp = requests.post(
                f"{moss_url.rstrip('/')}/v1/audio/transcriptions",
                files={"file": (src_path.name, f)},
                data=data,
                timeout=7200,
            )
    except requests.ConnectionError as exc:
        raise HTTPException(
            status_code=502,
            detail=f"MOSS backend unreachable at {moss_url} — is the MOSS server running?",
        ) from exc
    if resp.status_code != 200:
        raise HTTPException(
            status_code=502,
            detail=f"MOSS backend error {resp.status_code}: {resp.text[:300]}",
        )
    return [
        {
            "start": float(s["start"]),
            "end": float(s["end"]),
            "cluster": str(s["speaker"]),
            "text": s["text"].strip(),
        }
        for s in resp.json().get("segments", [])
        if s["text"].strip()
    ]


# --------------------------------------------------------------------------- protocol

def _usage(duration: float) -> dict[str, Any]:
    return {"type": "duration", "seconds": round(duration, 2)}


def _segment_dict(index: int, seg: dict[str, Any]) -> dict[str, Any]:
    return {
        "id": f"seg_{index:04d}",
        "type": "transcript.text.segment",
        "start": seg["start"],
        "end": seg["end"],
        "speaker": seg["speaker"],
        "text": seg["text"],
    }


def _build_response(
    response_format: str,
    segments: list[dict[str, Any]],
    duration: float,
    language: str | None,
) -> Any:
    text = join_segment_texts([s["text"] for s in segments])
    usage = _usage(duration)
    if response_format == "text":
        return text
    if response_format == "json":
        return {"text": text, "usage": usage}
    if response_format == "diarized_json":
        return {
            "task": "transcribe",
            "duration": duration,
            "text": text,
            "segments": [_segment_dict(i + 1, s) for i, s in enumerate(segments)],
            "usage": usage,
        }
    return {  # verbose_json: Whisper-style with speaker on each segment
        "task": "transcribe",
        "language": language or "zh",
        "duration": duration,
        "text": text,
        "segments": [
            {"id": i, "start": s["start"], "end": s["end"],
             "text": s["text"], "speaker": s["speaker"]}
            for i, s in enumerate(segments)
        ],
        "usage": usage,
    }


def _sse_event(payload: dict[str, Any]) -> str:
    return f"data: {json.dumps(payload, ensure_ascii=False)}\n\n"


def _getlist(form, name: str) -> list[Any]:
    return list(form.getlist(f"{name}[]")) + list(form.getlist(name))


# --------------------------------------------------------------------------- app

def create_app(engine: Engine, moss_url: str, refine: bool = True) -> FastAPI:
    app = FastAPI(title="funasr-openai-compat")

    @app.get("/v1/models")
    def models():
        return {
            "object": "list",
            "data": [
                {"id": "gpt-4o-transcribe-diarize", "object": "model", "owned_by": "funasr"},
                {"id": "funasr-paraformer-diarize", "object": "model", "owned_by": "funasr"},
                {"id": "fun-asr-nano-diarize", "object": "model", "owned_by": "funaudiollm"},
                {"id": "moss-transcribe-diarize", "object": "model", "owned_by": "openmoss"},
            ],
        }

    @app.post("/v1/audio/transcriptions")
    async def transcriptions(request: Request):
        form = await request.form()

        for field in ("timestamp_granularities", "include"):
            if _getlist(form, field):
                raise HTTPException(
                    status_code=400,
                    detail=f"'{field}' is not supported by gpt-4o-transcribe-diarize.",
                )

        upload = form.get("file")
        if not isinstance(upload, UploadFile):
            raise HTTPException(status_code=400, detail="Field 'file' (audio upload) is required.")

        response_format = str(form.get("response_format") or "json")
        if response_format not in RESPONSE_FORMATS:
            raise HTTPException(
                status_code=400,
                detail=f"Unsupported response_format '{response_format}'. "
                f"Supported: {sorted(RESPONSE_FORMATS)}.",
            )
        stream = str(form.get("stream") or "").lower() in {"1", "true", "yes"}
        model_name = str(form.get("model") or "fun-asr-nano-diarize")
        language = str(form.get("language")) if form.get("language") else None
        # prompt / temperature / chunking_strategy: accepted for protocol
        # compatibility; Paraformer has no use for them.

        names = [str(n) for n in _getlist(form, "known_speaker_names")]
        references = _getlist(form, "known_speaker_references")
        if len(names) != len(references):
            raise HTTPException(
                status_code=400,
                detail="known_speaker_names and known_speaker_references must have "
                "the same number of entries.",
            )
        if len(names) > engine.max_known_speakers:
            raise HTTPException(
                status_code=400,
                detail=f"At most {engine.max_known_speakers} known speakers are supported.",
            )

        tmpdir = Path(tempfile.mkdtemp(prefix="funasr_api_"))
        suffix = Path(upload.filename or "audio.wav").suffix or ".wav"
        src_path = tmpdir / f"input{suffix}"
        with src_path.open("wb") as f:
            while chunk := await upload.read(1024 * 1024):
                f.write(chunk)
        wav_path = tmpdir / "input_16k.wav"
        await run_in_threadpool(ffmpeg_to_16k, src_path, wav_path)

        ref_bytes = [_reference_bytes(ref, i) for i, ref in enumerate(references)]

        def _prepare_known() -> dict[str, np.ndarray]:
            known = engine.enrolled_speakers()  # server-side DB first, request overrides
            for i, (name, raw) in enumerate(zip(names, ref_bytes)):
                known[name] = engine.embed_reference(raw, i, tmpdir)
            return known

        engine_key = model_name.lower()
        use_moss = "moss" in engine_key
        # Nano is the default engine; Paraformer only when explicitly named. The
        # OpenAI-compatible alias "gpt-4o-transcribe-diarize" therefore lands on
        # Nano too, so a client that keeps the official model name does not
        # silently pull in a second 1.6GB pipeline.
        use_nano = not use_moss and "paraformer" not in engine_key
        # Local-only extension: domain terms biasing the decoder. SeACo-Paraformer
        # takes them natively; MOSS takes them through its prompt.
        raw_hot = str(form.get("hotwords") or "")
        hotwords = [w for w in raw_hot.replace("\n", ",").replace("，", ",").split(",") if w.strip()]
        hotwords = [w.strip() for w in hotwords]
        prompt = str(form.get("prompt") or "") or None

        def _run() -> tuple[list[dict[str, Any]], float]:
            known = _prepare_known()
            if use_moss:
                sentences = moss_transcribe(src_path, moss_url, language, hotwords, prompt)
            elif use_nano:
                sentences = engine.transcribe_nano(wav_path, hotwords)
                if refine:
                    sentences = engine.refine_segments(sentences, wav_path, tmpdir)
            else:
                sentences = engine.transcribe(wav_path, hotwords)
            engine.label_speakers(sentences, wav_path, known, tmpdir)
            duration = float(sf.info(wav_path).duration)
            return sentences, round(duration, 2)

        def _cleanup() -> None:
            for p in tmpdir.iterdir():
                p.unlink(missing_ok=True)
            tmpdir.rmdir()

        if stream:
            def generate() -> Iterator[str]:
                try:
                    sentences, duration = _run()
                    for i, seg in enumerate(sentences, 1):
                        yield _sse_event({
                            "type": "transcript.text.delta",
                            "delta": seg["text"],
                            "segment_id": f"seg_{i:04d}",
                        })
                        if response_format == "diarized_json":
                            yield _sse_event(_segment_dict(i, seg))
                    yield _sse_event({
                        "type": "transcript.text.done",
                        "text": join_segment_texts([s["text"] for s in sentences]),
                        "usage": _usage(duration),
                    })
                except HTTPException as exc:
                    yield _sse_event({"type": "error", "error": {"message": exc.detail}})
                except Exception as exc:  # noqa: BLE001
                    yield _sse_event({"type": "error", "error": {"message": str(exc)}})
                finally:
                    _cleanup()

            return StreamingResponse(
                generate(),
                media_type="text/event-stream",
                headers={"Cache-Control": "no-store", "X-Accel-Buffering": "no"},
            )

        try:
            sentences, duration = await run_in_threadpool(_run)
        except HTTPException:
            raise
        except Exception as exc:  # noqa: BLE001
            raise HTTPException(status_code=500, detail=str(exc)) from exc
        finally:
            _cleanup()

        payload = _build_response(response_format, sentences, duration, language)
        if response_format == "text":
            return PlainTextResponse(str(payload))
        return JSONResponse({**payload, "model": model_name})

    return app


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8100)
    parser.add_argument(
        "--speaker-db",
        default=str(Path(__file__).parent / "speaker_db.json"),
        help="enroll_speaker.py JSON db; matched on every request. '' disables.",
    )
    parser.add_argument(
        "--sv-threshold", type=float, default=0.3,
        help="cosine similarity threshold for voiceprint matching (official wss server uses 0.2)",
    )
    parser.add_argument(
        "--speech-noise-thres", type=float, default=0.2,
        help="FSMN-VAD speech/noise threshold. Lower keeps quiet far-field speech "
        "(FunASR's default drops it); raise toward 0.6 if noise leaks in as text.",
    )
    parser.add_argument(
        "--max-end-silence-time", type=int, default=1200,
        help="ms of silence before VAD closes a segment (FunASR default 800)",
    )
    parser.add_argument(
        "--no-refine-nano", action="store_true",
        help="disable punctuation-level speaker re-attribution for Fun-ASR-Nano "
        "(faster, but a speaker change inside one VAD segment is lost)",
    )
    parser.add_argument(
        "--moss-url", default="http://127.0.0.1:7860",
        help="MOSS-Transcribe-Diarize server for model=moss-transcribe-diarize requests",
    )
    parser.add_argument(
        "--max-known-speakers", type=int, default=4,
        help="max known_speaker_* entries per request. Default 4 mirrors the official "
        "OpenAI limit; raise it only for local-only clients (requests with >4 "
        "speakers will fail on api.openai.com).",
    )
    args = parser.parse_args()

    print("[startup] loading FunASR pipeline (paraformer-zh + vad + punc + cam++)...", flush=True)
    engine = Engine(
        speaker_db_path=Path(args.speaker_db) if args.speaker_db else None,
        sv_threshold=args.sv_threshold,
        max_known_speakers=args.max_known_speakers,
        speech_noise_thres=args.speech_noise_thres,
        max_end_silence_time=args.max_end_silence_time,
    )
    print("[startup] models ready", flush=True)

    import uvicorn

    uvicorn.run(
        create_app(engine, args.moss_url, not args.no_refine_nano),
        host=args.host, port=args.port, log_level="info",
    )


if __name__ == "__main__":
    main()
