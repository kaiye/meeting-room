// REST + WebSocket client: api() does request/response, onWSMessage()
// subscribes to server-pushed events.

export type Meeting = {
  id: string
  title: string
  status: 'created' | 'recording' | 'processing' | 'completed' | 'failed'
  language?: string
  durationSeconds?: number
  source: 'live' | 'import'
  titleCustomized: boolean
  participants: string[]
  hasAudio: boolean
  context: string
  calendarUid: string
  createdAt: string
  updatedAt: string
}

export type Comment = {
  id: string
  meetingId: string
  target: 'transcript' | 'summary' | 'general'
  kind: 'rewrite' | 'comment' | 'delete' | 'note'
  quote: string
  body: string
  author: string
  anchor: string
  revisionGroupId: string
  createdAt: string
}

export type SpeakerClip = {
  id: string
  speakerId: string
  sourceMeetingId?: string
  seconds: number
  createdAt: string
}

export type Segment = {
  id: number
  startMs: number
  endMs: number
  speaker?: string
  text: string
  windowIdx: number
}

export type Job = {
  id: string
  meetingId: string
  type: string
  status: 'queued' | 'running' | 'completed' | 'failed'
  progress: number
  error?: string
  cancelRequested?: boolean
  startedAt?: string
}

export type Summary = {
  meetingId: string
  template: string
  customPrompt: string
  content: string
  model?: string
  status: 'generating' | 'completed' | 'failed'
  error?: string
  prompt: string
  input: string
  updatedAt: string
}

export type Template = { id: string; name: string; description: string }

export type Speaker = {
  id: string
  name: string
  aliases: string[]
  nameEn: string
  email: string
  displayName: string
  clipCount: number
  sampleSeconds: number
  createdAt: string
  updatedAt: string
}

export type Settings = {
  transcribe: {
    baseUrl: string
    model: string
    apiKey: string
    liveBaseUrl: string
    liveModel: string
    liveApiKey: string
    language: string
    chunkSeconds: number
    liveChunkSeconds: number
  }
  summary: { baseUrl: string; model: string; apiKey: string }
  context: { background: string; glossary: string }
  calendar: { icsUrl: string }
}

export type CalendarEvent = {
  uid: string
  title: string
  startMs: number
  endMs: number
  allDay: boolean
  location: string
  attendees: Array<{ name: string; email: string }>
  description: string
}

export async function api<T = any>(path: string, opts?: RequestInit): Promise<T> {
  const init: RequestInit = { ...opts }
  if (init.body && !(init.body instanceof FormData)) {
    init.headers = { 'Content-Type': 'application/json', ...(init.headers as any) }
  }
  const r = await fetch('/api/v1' + path, init)
  if (!r.ok) {
    let msg = 'HTTP ' + r.status
    try {
      msg = (await r.json()).error || msg
    } catch {}
    throw new Error(msg)
  }
  return r.json()
}

// ---- WebSocket event bus (server-push: job-progress / live-transcript /
// live-error / summary-update) ----

export type WSMessage = {
  type: string
  meetingId?: string
  jobId?: string
  jobType?: string
  status?: string
  progress?: number
  message?: string
  error?: string
  title?: string
  segments?: Array<{ id?: number; startMs: number; endMs: number; speaker?: string; text: string; windowIdx?: number }>
}

type Listener = (msg: WSMessage) => void
const listeners = new Set<Listener>()
let ws: WebSocket | null = null

export function ensureWS() {
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return
  const proto = location.protocol === 'https:' ? 'wss://' : 'ws://'
  ws = new WebSocket(proto + location.host + '/api/v1/ws')
  ws.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data)
      listeners.forEach((fn) => fn(msg))
    } catch {}
  }
  ws.onclose = () => {
    ws = null
    setTimeout(ensureWS, 3000)
  }
}

export function onWSMessage(fn: Listener): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

export function fmtTs(ms: number): string {
  const s = Math.floor(ms / 1000)
  return `${String(Math.floor(s / 60)).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}`
}

const speakerPalette = ['#2563eb', '#dc2626', '#059669', '#d97706', '#7c3aed', '#0891b2', '#be185d', '#4d7c0f']
const speakerColors: Record<string, string> = {}
export function speakerColor(sp: string): string {
  if (!speakerColors[sp]) speakerColors[sp] = speakerPalette[Object.keys(speakerColors).length % speakerPalette.length]
  return speakerColors[sp]
}
