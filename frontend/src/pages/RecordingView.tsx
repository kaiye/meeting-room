// Recording view — desktop page.tsx equivalent: full-width transcript
// column (centered, max-w-750), sticky recording status bar, floating
// bottom control pill (mic / pause / stop + waveform bars).
import { CalendarDays, Copy, FileText, Loader2, Mic, MonitorSpeaker, Pause, Play, Plus, RefreshCw, Square } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, fmtTs, onWSMessage, speakerColor, type CalendarEvent, type Meeting, type Segment, type Speaker } from '../api'
import { useApp } from '../App'
import ParticipantPicker from '../components/ParticipantPicker'
import SpeakerEnrollDialog from '../components/SpeakerEnrollDialog'
import { recorder } from '../recorder'

type Phase = 'idle' | 'starting' | 'recording' | 'paused' | 'stopping'

export default function RecordingView() {
  const navigate = useNavigate()
  const { meetings } = useApp()
  const [phase, setPhase] = useState<Phase>(recorder.state === 'idle' ? 'idle' : 'recording')
  const [segments, setSegments] = useState<Segment[]>([])
  const [elapsed, setElapsed] = useState(0)
  const [error, setError] = useState('')
  const [tabAudio, setTabAudio] = useState(false)
  const [bars, setBars] = useState([12, 20, 8])
  const [speakers, setSpeakers] = useState<Speaker[]>([])
  const [meetingContext, setMeetingContext] = useState('')
  // Today's calendar (subscribed ICS feed): click an event to start with
  // its title / time / attendees prefilled.
  const [calendar, setCalendar] = useState<{ configured: boolean; error?: string; events: CalendarEvent[] } | null>(null)
  const [selectedEvent, setSelectedEvent] = useState<string | null>(null)
  const [calLoading, setCalLoading] = useState(false)
  const pendingTitleRef = useRef('')
  const autoPickedRef = useRef(false)
  // Who is in the room: only their voiceprints ride along. Remembered
  // across meetings; empty = no voiceprints (raw letter labels).
  const [participants, setParticipants] = useState<string[]>(() => {
    try {
      return JSON.parse(localStorage.getItem('meeting-room-participants') || '[]')
    } catch {
      return []
    }
  })
  const [labeling, setLabeling] = useState<Segment | null>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const stickRef = useRef(true)
  const meetingIdRef = useRef(recorder.meetingId)

  // Render today's calendar instantly from the browser cache, then refresh
  // from the server (the ICS fetch takes a moment — hence the indicator).
  const loadCalendar = useCallback(async (force = false) => {
    setCalLoading(true)
    try {
      const data = await api<{ configured: boolean; error?: string; events: CalendarEvent[] }>(
        `/calendar/today${force ? '?force=1' : ''}`,
      )
      setCalendar(data)
      localStorage.setItem('meeting-room-calendar-today', JSON.stringify({ day: new Date().toDateString(), data }))
    } catch (e) {
      console.error(e)
    }
    setCalLoading(false)
  }, [])

  useEffect(() => {
    api<Speaker[]>('/speakers').then(setSpeakers).catch(console.error)
    try {
      const raw = localStorage.getItem('meeting-room-calendar-today')
      if (raw) {
        const cached = JSON.parse(raw)
        if (cached.day === new Date().toDateString()) setCalendar(cached.data)
      }
    } catch {}
    loadCalendar()
  }, [loadCalendar])

  const fmtHM = (ms: number) => new Date(ms).toTimeString().slice(0, 5)


  // The event to preselect: the one happening now, else the next one
  // starting within the day, else the last one that ended.
  const currentEventIdx = (events: CalendarEvent[]): number => {
    const now = Date.now()
    let ongoing = -1
    let next = -1
    let past = -1
    events.forEach((ev, i) => {
      if (ev.allDay) return
      if (ev.startMs <= now && now < ev.endMs) {
        if (ongoing < 0) ongoing = i
      } else if (ev.startMs > now) {
        if (next < 0) next = i
      } else {
        past = i
      }
    })
    void past
    return ongoing >= 0 ? ongoing : next // never preselect a finished event
  }

  // Prefill title / context from a calendar event and resolve its
  // attendees to members — unknown ones are created on the fly (profile
  // only, no voiceprint) so the roster is complete. Returns the applied
  // values so "start now" needn't wait for state to settle.
  const applyEvent = async (ev: CalendarEvent) => {
    setSelectedEvent(ev.uid)
    pendingTitleRef.current = ev.title
    let ctx = ev.title
    if (!ev.allDay) ctx += `\n时间: ${fmtHM(ev.startMs)} – ${fmtHM(ev.endMs)}`
    if (ev.location) ctx += `\n地点: ${ev.location}`
    if (ev.attendees.length > 0) ctx += `\n参会人: ${ev.attendees.map((a) => a.name).join('、')}`
    if (ev.description) ctx += `\n\n${ev.description}`
    setMeetingContext(ctx)

    // Match attendees against the team roster — never create members from
    // a calendar invite; the roster is curated in 成员管理.
    const names: string[] = []
    for (const sp of speakers) {
      const keys = [sp.name, ...(sp.aliases ?? []), sp.nameEn, sp.email]
        .filter(Boolean)
        .map((k) => k.toLowerCase())
      const hit = ev.attendees.some((a) => {
        const email = (a.email || '').toLowerCase()
        const name = (a.name || '').toLowerCase()
        return keys.some((k) => k === email || k === name)
      })
      if (hit) names.push(sp.name)
    }
    if (ev.attendees.length > 0) {
      setParticipants(names)
      localStorage.setItem('meeting-room-participants', JSON.stringify(names))
    }
    return { title: ev.title, context: ctx, participants: names.length > 0 ? names : undefined, calendarUid: ev.uid }
  }

  // Auto-select the event happening now (or up next) once the calendar and
  // member list are loaded.
  useEffect(() => {
    if (autoPickedRef.current || phase !== 'idle') return
    if (!calendar?.configured || calendar.events.length === 0) return
    if (speakers.length === 0) return // roster not loaded yet — matching would find nobody
    const idx = currentEventIdx(calendar.events)
    if (idx < 0) return
    autoPickedRef.current = true
    applyEvent(calendar.events[idx])
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [calendar, phase, speakers])

  const toggleParticipant = (name: string) => {
    setParticipants((prev) => {
      const next = prev.includes(name) ? prev.filter((n) => n !== name) : [...prev, name]
      localStorage.setItem('meeting-room-participants', JSON.stringify(next))
      return next
    })
  }

  // Waveform eye-candy + elapsed timer, mirrors desktop's 300ms randomizer.
  useEffect(() => {
    if (phase !== 'recording' && phase !== 'paused') return
    const t = setInterval(() => {
      if (phase === 'recording') setBars([8 + Math.random() * 16, 8 + Math.random() * 16, 8 + Math.random() * 16])
      setElapsed(recorder.elapsedMs)
    }, 300)
    return () => clearInterval(t)
  }, [phase])

  // Live transcript stream for the in-flight meeting.
  useEffect(
    () =>
      onWSMessage((msg) => {
        if (msg.type === 'live-transcript' && msg.meetingId === meetingIdRef.current && msg.segments) {
          setSegments((prev) => [
            ...prev,
            ...msg.segments!.map((s, i) => ({ id: prev.length + i, ...s }) as Segment),
          ])
        }
        if (msg.type === 'live-error' && msg.meetingId === meetingIdRef.current) {
          setError('转录出错(录音继续,可稍后重转): ' + msg.error)
        }
      }),
    [],
  )

  useEffect(() => {
    if (stickRef.current && scrollRef.current) scrollRef.current.scrollTop = scrollRef.current.scrollHeight
  }, [segments])

  const start = useCallback(
    async (ov?: { title?: string; context?: string; participants?: string[]; calendarUid?: string }) => {
      if (recorder.state !== 'idle') return
      setError('')
      setPhase('starting')
      try {
        const now = new Date()
        const p = (n: number) => String(n).padStart(2, '0')
        const title =
          ov?.title || pendingTitleRef.current || `会议 ${p(now.getMonth() + 1)}-${p(now.getDate())} ${p(now.getHours())}:${p(now.getMinutes())}`
        recorder.onStopped = (id) => navigate(`/meeting/${id}?source=recording`)
        // Language comes from 设置 → 转录服务; the server fills the default.
        const valid = new Set(speakers.map((s) => s.name))
        const id = await recorder.start({
          title,
          language: '',
          tabAudio,
          participants: (ov?.participants ?? participants).filter((p) => valid.has(p)),
          context: (ov?.context ?? meetingContext).trim(),
          calendarUid: ov?.calendarUid ?? selectedEvent ?? '',
        })
        meetingIdRef.current = id
        setSegments([])
        setMeetingContext('')
        pendingTitleRef.current = ''
        setPhase('recording')
      } catch (e: any) {
        setError(e.message)
        setPhase('idle')
      }
    },
    [tabAudio, navigate, speakers, participants, meetingContext, selectedEvent],
  )

  useEffect(() => {
    const h = () => start()
    window.addEventListener('meeting-room-start-recording', h)
    return () => window.removeEventListener('meeting-room-start-recording', h)
  }, [start])

  const stop = () => {
    setPhase('stopping')
    recorder.stop()
  }

  const copyAll = () => {
    navigator.clipboard.writeText(
      segments.map((s) => `[${fmtTs(s.startMs)}] ${s.speaker ? s.speaker + ': ' : ''}${s.text}`).join('\n'),
    )
  }

  const onScroll = () => {
    const el = scrollRef.current
    if (!el) return
    stickRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 100
  }

  // Merge today's calendar events with today's recordings: an event that
  // was recorded shows as one row; ad-hoc recordings are inserted by time.
  const timeline = (() => {
    const today = new Date().toDateString()
    const todays = meetings.filter((m) => new Date(m.createdAt).toDateString() === today)
    const used = new Set<string>()
    const items: Array<{ key: string; sortMs: number; event?: CalendarEvent; meeting?: Meeting }> = []
    for (const ev of calendar?.events ?? []) {
      // Exact link first; older recordings (made before linking existed)
      // fall back to same title started within the event's window.
      const m =
        todays.find((x) => x.calendarUid && x.calendarUid === ev.uid) ??
        todays.find((x) => {
          if (x.calendarUid || used.has(x.id)) return false
          const t = new Date(x.createdAt).getTime()
          return x.title.trim() === ev.title.trim() && t >= ev.startMs - 30 * 60000 && t <= ev.endMs + 30 * 60000
        })
      if (m) used.add(m.id)
      items.push({ key: ev.uid || ev.title + ev.startMs, sortMs: ev.startMs, event: ev, meeting: m })
    }
    for (const m of todays) {
      if (!used.has(m.id)) items.push({ key: m.id, sortMs: new Date(m.createdAt).getTime(), meeting: m })
    }
    return items.sort((a, b) => a.sortMs - b.sortMs)
  })()

  const recordingActive = phase === 'recording' || phase === 'paused'

  const controlPill = (
        <div className="flex items-center gap-3 rounded-full bg-white px-5 py-3 shadow-lg ring-1 ring-gray-200">
          {phase === 'idle' || phase === 'starting' ? (
            <>
              <button
                onClick={() => setTabAudio(!tabAudio)}
                title={tabAudio ? '将同时采集标签页音频' : '仅采集麦克风(点击加录标签页音频)'}
                className={`flex h-9 w-9 items-center justify-center rounded-full border ${tabAudio ? 'border-blue-500 bg-blue-50 text-blue-600' : 'border-gray-200 text-gray-400 hover:text-gray-600'}`}
              >
                <MonitorSpeaker size={16} />
              </button>
              <button
                onClick={() => start()}
                disabled={phase === 'starting'}
                title="开始录音"
                className="flex h-12 w-12 items-center justify-center rounded-full bg-red-500 text-white shadow hover:bg-red-600 disabled:opacity-60"
              >
                {phase === 'starting' ? (
                  <span className="h-5 w-5 animate-spin rounded-full border-2 border-white border-t-transparent" />
                ) : (
                  <Mic size={20} />
                )}
              </button>
            </>
          ) : phase === 'stopping' ? (
            <span className="flex items-center gap-2 px-2 text-sm text-gray-500">
              <span className="h-4 w-4 animate-spin rounded-full border-2 border-gray-300 border-t-gray-600" />
              正在停止…
            </span>
          ) : (
            <>
              <button
                onClick={() => (phase === 'paused' ? (recorder.resume(), setPhase('recording')) : (recorder.pause(), setPhase('paused')))}
                title={phase === 'paused' ? '继续' : '暂停'}
                className="flex h-10 w-10 items-center justify-center rounded-full border-2 border-gray-300 text-gray-600 hover:bg-gray-50"
              >
                {phase === 'paused' ? <Play size={16} /> : <Pause size={16} />}
              </button>
              <button
                onClick={stop}
                title="停止录音"
                className="flex h-10 w-10 items-center justify-center rounded-full bg-red-500 text-white hover:bg-red-600"
              >
                <Square size={14} />
              </button>
              <div className="flex h-6 items-end gap-1 px-1">
                {bars.map((h, i) => (
                  <span
                    key={i}
                    style={{ height: `${phase === 'paused' ? 6 : h}px` }}
                    className={`w-1 rounded transition-all ${phase === 'paused' ? 'bg-orange-500 opacity-60' : 'bg-red-500'}`}
                  />
                ))}
              </div>
            </>
          )}
        </div>
  )

  return (
    <div className="flex h-full flex-col">
      {/* Header — copy action; transcription language lives in 设置 */}
      {segments.length > 0 && (
        <div className="flex items-center justify-end gap-2 border-b border-gray-200 bg-white px-4 py-2">
          <button onClick={copyAll} className="flex items-center gap-1.5 rounded-md border border-gray-200 px-3 py-1.5 text-sm text-gray-600 hover:bg-gray-50">
            <Copy size={14} /> 复制
          </button>
        </div>
      )}

      {/* Body: calendar column (idle) + transcript column */}
      <div className="flex min-h-0 flex-1">
      {/* Today's timeline (idle only): calendar events and today's
          recordings merged into one chronological column. */}
      {phase === 'idle' && (
        <div className="hidden w-80 shrink-0 flex-col border-r border-gray-200 bg-white lg:flex">
          <div className="flex items-center gap-2 border-b border-gray-100 px-3 py-2">
            <CalendarDays size={15} className="text-blue-500" />
            <span className="text-sm font-medium text-gray-700">今天</span>
            <span className="ml-auto flex items-center gap-1.5 text-xs text-gray-400">
              {calLoading ? (
                <span className="flex items-center gap-1 text-blue-500">
                  <Loader2 size={12} className="animate-spin" /> 同步日历…
                </span>
              ) : (
                <>
                  {new Date().toLocaleDateString('zh-CN', { month: 'long', day: 'numeric', weekday: 'short' })}
                  <button
                    onClick={() => loadCalendar(true)}
                    title="立即同步 Google 日历"
                    className="rounded p-0.5 text-gray-400 hover:bg-gray-100 hover:text-blue-600"
                  >
                    <RefreshCw size={12} />
                  </button>
                </>
              )}
            </span>
          </div>
          <div className="custom-scrollbar min-h-0 flex-1 overflow-y-auto p-2">
            {calendar?.error && (
              <p className="mb-2 rounded-md bg-red-50 px-2.5 py-1.5 text-xs text-red-600">日历读取失败:{calendar.error}</p>
            )}
            {timeline.length === 0 && (
              <p className="flex items-center justify-center gap-1.5 py-10 text-center text-xs text-gray-400">
                {calLoading && !calendar ? (
                  <>
                    <Loader2 size={12} className="animate-spin" /> 正在读取日历…
                  </>
                ) : calendar?.configured ? (
                  '今天还没有日程和录音'
                ) : (
                  '今天还没有录音'
                )}
              </p>
            )}
            {timeline.map((it) => {
              const ev = it.event
              const mt = it.meeting
              const active = ev && selectedEvent === ev.uid
              const live = mt?.status === 'recording'
              return (
                <div
                  key={it.key}
                  onClick={() => (mt ? navigate(`/meeting/${mt.id}`) : ev && applyEvent(ev))}
                  className={`group mb-1.5 cursor-pointer rounded-md border px-2.5 py-2 ${active ? 'border-blue-400 bg-blue-50/60' : 'border-gray-100 hover:border-gray-300'}`}
                >
                  <div className="flex items-center gap-2">
                    <span className={`shrink-0 text-xs font-medium ${mt ? 'text-gray-400' : 'text-blue-600'}`}>
                      {ev ? (ev.allDay ? '全天' : `${fmtHM(ev.startMs)}–${fmtHM(ev.endMs)}`) : fmtHM(new Date(mt!.createdAt).getTime())}
                    </span>
                    <span className="min-w-0 flex-1 truncate text-sm text-gray-800">{ev?.title || mt?.title}</span>
                    {live ? (
                      <span className="pulse-dot h-2 w-2 shrink-0 rounded-full bg-red-500" title="录音中" />
                    ) : mt ? (
                      <FileText size={12} className="shrink-0 text-gray-300" />
                    ) : (
                      <button
                        onClick={async (e) => {
                          e.stopPropagation()
                          start(await applyEvent(ev!))
                        }}
                        title="以此日程开始录音"
                        className="shrink-0 rounded-full bg-red-500 p-1.5 text-white opacity-0 hover:bg-red-600 group-hover:opacity-100"
                      >
                        <Mic size={12} />
                      </button>
                    )}
                  </div>
                  <p className="mt-0.5 truncate text-[11px] text-gray-400">
                    {mt
                      ? `已录制${mt.durationSeconds ? ` · ${Math.round(mt.durationSeconds / 60)} 分钟` : ''}${ev ? ' · 来自日程' : ''}`
                      : [ev!.attendees.length > 0 ? `${ev!.attendees.length} 人` : '', ev!.location].filter(Boolean).join(' · ')}
                  </p>
                </div>
              )
            })}
          </div>
          <button
            onClick={() => {
              setSelectedEvent(null)
              pendingTitleRef.current = ''
              setMeetingContext('')
              setParticipants([])
              localStorage.setItem('meeting-room-participants', '[]')
            }}
            className="m-2 flex items-center justify-center gap-1.5 rounded-md border border-dashed border-gray-300 py-2 text-sm text-gray-500 hover:border-blue-400 hover:text-blue-600"
          >
            <Plus size={14} /> 新建会议
          </button>
        </div>
      )}

      {/* Transcript body */}
      <div ref={scrollRef} onScroll={onScroll} className="custom-scrollbar min-h-0 flex-1 overflow-y-auto pb-40">
        <div className="mx-auto w-11/12 max-w-[750px] pt-4">
          {recordingActive && (
            <div className="sticky top-0 z-10 mb-3 flex items-center gap-2 rounded-md bg-white/90 px-3 py-2 text-sm shadow-sm backdrop-blur">
              <span className={`h-2.5 w-2.5 rounded-full ${phase === 'paused' ? 'bg-orange-500' : 'pulse-dot bg-red-500'}`} />
              <span className="font-medium text-gray-700">
                {phase === 'paused' ? '已暂停' : '录音中'} • {fmtTs(elapsed)}
              </span>
              <span className="ml-auto text-xs text-gray-400">字幕按窗口滚动出现,有几分钟延迟</span>
            </div>
          )}

          {error && <div className="mb-3 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">{error}</div>}

          {segments.length === 0 && !recordingActive && phase !== 'stopping' && (
            <div className="mt-24 text-center text-gray-400">
              <p className="text-lg font-medium text-gray-500">欢迎使用 Meeting Room</p>
              {speakers.length > 0 && (
                <div className="mx-auto mt-8 max-w-md text-left">
                  <p className="mb-2 text-xs font-medium text-gray-400">参会人</p>
                  <ParticipantPicker speakers={speakers} selected={participants} onToggle={toggleParticipant} />
                </div>
              )}
              <div className="mx-auto mt-5 max-w-md">
                <p className="mb-2 text-left text-xs font-medium text-gray-400">会议背景(可选,给 AI 提供上下文)</p>
                <textarea
                  value={meetingContext}
                  onChange={(e) => setMeetingContext(e.target.value)}
                  placeholder={'可直接粘贴日历邀请、议程、参会人名单等'}
                  rows={7}
                  className="w-full resize-y rounded-md border border-gray-200 px-3 py-2 text-left text-sm leading-relaxed outline-none focus:border-blue-400"
                />
              </div>
              <div className="mt-6 flex justify-center">{controlPill}</div>
            </div>
          )}
          {segments.length === 0 && recordingActive && (
            <div className="mt-24 flex flex-col items-center gap-2 text-gray-400">
              <span className="pulse-dot h-3 w-3 rounded-full bg-red-400" />
              <p className="text-sm">{phase === 'paused' ? '录音已暂停' : '正在聆听…'}</p>
            </div>
          )}
          {phase === 'stopping' && (
            <div className="mt-8 text-center text-sm text-gray-500">正在保存并补全最后一段转录…即将跳转</div>
          )}

          {segments.map((s, i) => (
            <div key={s.id} className={`mb-1 flex gap-3 px-1 py-1 ${i === segments.length - 1 ? 'rounded-lg border border-gray-200 bg-gray-100 px-3 py-2' : ''}`}>
              <span className="min-w-[50px] pt-0.5 text-xs text-gray-400">{fmtTs(s.startMs)}</span>
              {s.speaker && (
                <button
                  onClick={() => setLabeling(s)}
                  title="标注说话人:登记声纹后,后续实时字幕自动显示真名"
                  className="w-[60px] shrink-0 break-all rounded pt-0.5 text-left text-sm font-semibold leading-tight hover:underline"
                  style={{ color: speakerColor(s.speaker) }}
                >
                  {s.speaker}
                </button>
              )}
              <span className="text-base leading-relaxed text-gray-800">{s.text}</span>
            </div>
          ))}
          {recordingActive && segments.length > 0 && (
            <div className="flex items-center gap-2 px-1 py-3 text-sm text-gray-400">
              <span className="pulse-dot h-2 w-2 rounded-full bg-red-400" /> 聆听中…
            </div>
          )}
        </div>
      </div>
      </div>

      {labeling && meetingIdRef.current && (
        <SpeakerEnrollDialog
          meetingId={meetingIdRef.current}
          segment={labeling}
          counts={{
            all: segments.filter((s) => s.speaker === labeling.speaker).length,
            window: segments.filter((s) => s.speaker === labeling.speaker && s.windowIdx === labeling.windowIdx).length,
          }}
          onClose={() => setLabeling(null)}
          onEnrolled={(name, scope) =>
            setSegments((prev) =>
              prev.map((s) => {
                const hit =
                  scope === 'meeting'
                    ? s.speaker === labeling.speaker
                    : scope === 'window'
                      ? s.speaker === labeling.speaker && s.windowIdx === labeling.windowIdx
                      : s.id === labeling.id
                return hit ? { ...s, speaker: name } : s
              }),
            )
          }
        />
      )}

      {/* Recording controls float only while a session is live; when idle
          they sit right after the meeting form. */}
      {phase !== 'idle' && phase !== 'starting' && (
        <div className="absolute bottom-10 left-1/2 z-20 -translate-x-1/2">{controlPill}</div>
      )}
    </div>
  )
}
