"""Cut a speaker reference clip and print it as a data URL for known_speaker_references[].

Usage:
  python3 make_reference.py <audio> <start_seconds> [duration_seconds]

Only needs ffmpeg and the standard library -- no virtualenv required.
Measured on this stack: voiceprint discrimination peaks at 2-3s and declines
after, because longer clips accumulate shared room/channel characteristics that
raise different-speaker similarity too. Default 3s is the sweet spot.
"""
import base64
import subprocess
import sys
import tempfile
from pathlib import Path

def main() -> None:
    if len(sys.argv) not in (3, 4):
        sys.exit(__doc__)
    src, start = sys.argv[1], float(sys.argv[2])
    dur = float(sys.argv[3]) if len(sys.argv) == 4 else 3.0

    if not 2.0 <= dur <= 10.0:
        sys.exit(f"duration must be 2-10s (OpenAI hard limit), got {dur}")
    if dur > 3.0:
        print(f"warning: {dur}s exceeds the measured 2-3s optimum", file=sys.stderr)
    if not Path(src).exists():
        sys.exit(f"no such file: {src}")

    with tempfile.TemporaryDirectory() as tmp:
        wav = Path(tmp) / "ref.wav"
        proc = subprocess.run(
            ["ffmpeg", "-y", "-loglevel", "error", "-i", src,
             "-ss", str(start), "-t", str(dur), "-ac", "1", "-ar", "16000", str(wav)],
            capture_output=True, text=True,
        )
        if proc.returncode != 0:
            sys.exit(f"ffmpeg failed: {proc.stderr.strip()[:300]}")
        data = wav.read_bytes()

    if len(data) < 16000:  # < ~0.5s of 16k mono pcm16
        sys.exit(f"clip is nearly empty ({len(data)}B) -- is start={start}s past the end?")
    print("data:audio/wav;base64," + base64.b64encode(data).decode())
    print(f"ok: {dur}s from {start}s, {len(data)/1024:.1f}KB", file=sys.stderr)

if __name__ == "__main__":
    main()
