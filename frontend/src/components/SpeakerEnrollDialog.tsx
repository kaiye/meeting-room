// Speaker enrollment dialog: click a segment's speaker label, type the real
// name, and the server cuts that utterance (2-10s) from the recording as the
// voiceprint reference. From then on diarized transcription requests carry
// it, so this voice gets the real name across chunks — including the next
// rolling pass of a live meeting.
import { Loader2, Play, Square } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { api, fmtTs, type Segment, type Speaker } from '../api'
import Modal from './Modal'

export type EnrollScope = 'one' | 'window' | 'meeting'

export default function SpeakerEnrollDialog({
  meetingId,
  segment,
  counts,
  onClose,
  onEnrolled,
}: {
  meetingId: string
  segment: Segment
  // Same-label segment counts: whole meeting / within this segment's
  // transcription window (raw cluster labels are only stable per window).
  counts: { all: number; window: number }
  onClose: () => void
  onEnrolled: (name: string, scope: EnrollScope) => void
}) {
  const [name, setName] = useState('')
  const [speakers, setSpeakers] = useState<Speaker[]>([])
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [playing, setPlaying] = useState(false)
  const audioRef = useRef<HTMLAudioElement>(null)

  useEffect(() => {
    api<Speaker[]>('/speakers').then(setSpeakers).catch(console.error)
    return () => audioRef.current?.pause()
  }, [])

  // Play exactly this utterance so the user can confirm whose voice it is
  // before attaching the name.
  const togglePlay = () => {
    const el = audioRef.current
    if (!el) return
    if (playing) {
      el.pause()
      setPlaying(false)
      return
    }
    el.currentTime = segment.startMs / 1000
    el.play().then(() => setPlaying(true)).catch(() => {})
  }

  const onTimeUpdate = () => {
    const el = audioRef.current
    if (el && el.currentTime >= segment.endMs / 1000) {
      el.pause()
      setPlaying(false)
    }
  }

  // Real names come from voiceprints (globally stable); raw cluster labels
  // (A/B/C) are only "the same person" within one transcription window.
  const isRealName = speakers.some((sp) => sp.name === segment.speaker || sp.aliases.includes(segment.speaker ?? ''))
  const batchCount = isRealName || segment.windowIdx < 0 ? counts.all : counts.window

  const submit = async () => {
    if (!name.trim() || busy) return
    setBusy(true)
    setErr('')
    try {
      // Relabeling happens automatically: the server scopes it to the whole
      // meeting for voiceprint real names, or to this transcription window
      // for raw cluster labels.
      const r = await api<{ speaker: Speaker; scope: EnrollScope }>('/speakers/enroll', {
        method: 'POST',
        body: JSON.stringify({ meetingId, segmentId: segment.id, name: name.trim(), applyAll: true }),
      })
      onEnrolled(r.speaker.name, r.scope)
      onClose()
    } catch (e: any) {
      setErr(e.message)
      setBusy(false)
    }
  }

  return (
    <Modal
      title="标注说话人"
      onClose={onClose}
      footer={
        <>
          <button onClick={onClose} className="rounded-md px-4 py-2 text-sm text-gray-600 hover:bg-gray-100">
            取消
          </button>
          <button
            onClick={submit}
            disabled={!name.trim() || busy || segment.endMs - segment.startMs < 2000}
            className="flex items-center gap-1.5 rounded-md bg-blue-600 px-4 py-2 text-sm text-white hover:bg-blue-700 disabled:opacity-50"
          >
            {busy && <Loader2 size={14} className="animate-spin" />} 确认标注
          </button>
        </>
      }
    >
      <div className="mb-3 flex items-start gap-2 rounded-md bg-gray-50 px-3 py-2 text-sm text-gray-600">
        <button
          onClick={togglePlay}
          title={playing ? '停止' : '试听这句原始录音'}
          className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-blue-600 text-white hover:bg-blue-700"
        >
          {playing ? <Square size={10} /> : <Play size={10} className="ml-0.5" />}
        </button>
        <span className="min-w-0">
          <span className="mr-2 text-xs text-gray-400">[{fmtTs(segment.startMs)}]</span>
          {segment.text.length > 80 ? segment.text.slice(0, 80) + '…' : segment.text}
        </span>
        <audio ref={audioRef} preload="none" src={`/api/v1/meetings/${meetingId}/audio`} onTimeUpdate={onTimeUpdate} onEnded={() => setPlaying(false)} />
      </div>
      <label className="mb-1 block text-xs font-medium text-gray-500">这句话是谁说的?</label>
      <input
        autoFocus
        value={name}
        list="speaker-names"
        placeholder="输入姓名,如:王工"
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => e.key === 'Enter' && submit()}
        className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400"
      />
      <datalist id="speaker-names">
        {speakers.map((s) => (
          <option key={s.id} value={s.displayName || s.name} />
        ))}
      </datalist>
      {speakers.length > 0 && (
        <div className="mt-2 flex flex-wrap gap-1.5">
          {speakers.map((s) => (
            <button
              key={s.id}
              onClick={() => setName(s.displayName || s.name)}
              title={[s.name, s.nameEn, s.email].filter(Boolean).join(' · ')}
              className={`rounded-full border px-2.5 py-0.5 text-xs ${name === s.name ? 'border-blue-500 bg-blue-50 text-blue-600' : 'border-gray-200 text-gray-500 hover:border-gray-300'}`}
            >
              {s.displayName || s.name}
            </button>
          ))}
        </div>
      )}
      {batchCount > 1 && segment.speaker && (
        <div className="mt-3 rounded-md bg-blue-50/60 px-3 py-2 text-xs text-gray-600">
          {isRealName ? (
            <span>
              确认后,本会议所有「{segment.speaker}」的发言({counts.all} 句)将改为该姓名
              <span className="block text-[11px] text-gray-400">「{segment.speaker}」是声纹识别的真名,全场同名即同人</span>
            </span>
          ) : segment.windowIdx >= 0 ? (
            <span>
              确认后,同一转录窗口内「{segment.speaker}」的发言({counts.window} 句)将改为该姓名
              <span className="block text-[11px] text-gray-400">
                「{segment.speaker}」是窗口内的临时标签,其他窗口不受影响;标注声纹后点「重新转录」可全场按声纹归名
              </span>
            </span>
          ) : (
            <span>
              确认后,本会议所有「{segment.speaker}」的发言({counts.all} 句)将改为该姓名
              <span className="block text-[11px] text-gray-400">历史数据无窗口信息;字母标签跨分块可能不是同一个人,请抽查确认</span>
            </span>
          )}
        </div>
      )}
      {segment.endMs - segment.startMs < 2000 && (
        <p className="mt-3 rounded-md bg-red-50 px-3 py-2 text-xs text-red-600">这句话不足 2 秒,无法作为声纹样本,请挑一句更长的发言标注。</p>
      )}
      <p className="mt-3 text-xs leading-relaxed text-gray-400">
        将截取这句话的音频作为该说话人的唯一声纹样本(2-10 秒,超出自动截断到前 10 秒);重新标注会替换旧样本。之后的转录(包括本场会议接下来的实时字幕)会自动识别这个声音并标注真名;历史段落可通过「重新转录」统一更新。样本可在设置 → 说话人中试听和管理。
      </p>
      {err && <p className="mt-2 text-sm text-red-600">{err}</p>}
    </Modal>
  )
}
