// Participant picker shared by the home page and the meeting transcript
// pane: selected people as chips, everyone else added through a
// fuzzy-matching input (the roster is far too long to list inline).
import { Fingerprint, X } from 'lucide-react'
import { useMemo, useRef, useState } from 'react'
import { type Speaker } from '../api'

export default function ParticipantPicker({
  speakers,
  selected,
  onToggle,
  compact,
}: {
  speakers: Speaker[]
  // Canonical names (speaker.name) of the picked participants.
  selected: string[]
  onToggle: (name: string) => void
  compact?: boolean
}) {
  const [q, setQ] = useState('')
  const [open, setOpen] = useState(false)
  const boxRef = useRef<HTMLDivElement>(null)

  const picked = useMemo(
    () =>
      selected
        .map((n) => speakers.find((sp) => sp.name === n))
        .filter((sp): sp is Speaker => !!sp)
        .sort((a, b) => (b.sampleSeconds > 0 ? 1 : 0) - (a.sampleSeconds > 0 ? 1 : 0)),
    [speakers, selected],
  )

  const matches = useMemo(() => {
    const needle = q.trim().toLowerCase()
    return speakers
      .filter((sp) => !selected.includes(sp.name))
      .filter((sp) =>
        !needle
          ? true
          : [sp.displayName, sp.name, sp.nameEn, sp.email, ...(sp.aliases ?? [])]
              .filter(Boolean)
              .some((k) => k.toLowerCase().includes(needle)),
      )
      .sort((a, b) => (b.sampleSeconds > 0 ? 1 : 0) - (a.sampleSeconds > 0 ? 1 : 0))
      .slice(0, 8)
  }, [speakers, selected, q])

  const add = (sp: Speaker) => {
    onToggle(sp.name)
    setQ('')
  }

  const size = compact ? 'px-2 py-0.5 text-xs' : 'px-3 py-1 text-sm'

  return (
    <div className="flex flex-wrap items-center gap-1.5" ref={boxRef}>
      {picked.map((sp) => (
        <span
          key={sp.id}
          title={sp.sampleSeconds > 0 ? `声纹 ${sp.sampleSeconds.toFixed(1)}s` : '无声纹:识别为 A/B 标签'}
          className={`group flex items-center gap-1 rounded-full border border-blue-500 bg-blue-50 text-blue-600 ${size}`}
        >
          <Fingerprint size={compact ? 10 : 12} className={sp.sampleSeconds > 0 ? '' : 'opacity-30'} />
          {sp.displayName || sp.name}
          <button onClick={() => onToggle(sp.name)} title="移除" className="opacity-40 hover:opacity-100">
            <X size={compact ? 9 : 11} />
          </button>
        </span>
      ))}

      <div className="relative">
        <input
          value={q}
          onChange={(e) => {
            setQ(e.target.value)
            setOpen(true)
          }}
          onFocus={() => setOpen(true)}
          onBlur={() => setTimeout(() => setOpen(false), 150)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && matches.length > 0) add(matches[0])
            if (e.key === 'Escape') setOpen(false)
          }}
          placeholder="+ 添加参会人"
          className={`w-28 rounded-full border border-dashed border-gray-300 bg-transparent outline-none placeholder:text-gray-400 focus:w-40 focus:border-blue-400 ${size}`}
        />
        {open && matches.length > 0 && (
          <div className="absolute left-0 top-full z-30 mt-1 max-h-64 w-56 overflow-y-auto rounded-md border border-gray-200 bg-white py-1 shadow-lg">
            {matches.map((sp) => (
              <button
                key={sp.id}
                onMouseDown={(e) => {
                  e.preventDefault()
                  add(sp)
                }}
                className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-gray-50"
              >
                <Fingerprint size={12} className={sp.sampleSeconds > 0 ? 'text-blue-500' : 'text-gray-300'} />
                <span className="min-w-0 flex-1 truncate">{sp.displayName || sp.name}</span>
                <span className="shrink-0 text-[11px] text-gray-400">{sp.name !== sp.displayName ? sp.name : sp.nameEn}</span>
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
