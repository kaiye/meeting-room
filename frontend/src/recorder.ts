// Browser audio capture for live recording: mic (+ optional tab audio)
// -> AudioWorklet mixdown -> 16kHz s16le downsample -> WebSocket upstream.

const workletCode = `
class PCMCapture extends AudioWorkletProcessor {
  process(inputs) {
    const input = inputs[0];
    if (input && input.length && input[0].length) {
      const n = input[0].length;
      const mono = new Float32Array(n);
      for (let c = 0; c < input.length; c++) {
        const ch = input[c];
        for (let i = 0; i < n; i++) mono[i] += ch[i] / input.length;
      }
      this.port.postMessage(mono, [mono.buffer]);
    }
    return true;
  }
}
registerProcessor('pcm-capture', PCMCapture);
`

export type RecorderState = 'idle' | 'recording' | 'paused'

// Closing/refreshing the tab kills the capture pipeline; warn first so a
// meeting isn't cut off by a stray Cmd+W. (If the user insists, the server
// still finalizes whatever audio it has received.)
const warnBeforeUnload = (e: BeforeUnloadEvent) => {
  e.preventDefault()
  e.returnValue = ''
}

export class Recorder {
  state: RecorderState = 'idle'
  meetingId = ''
  // Voice-activity tracking for forgot-to-stop detection: refreshed
  // whenever a captured frame contains audible signal.
  private lastVoiceTs = 0
  private ws: WebSocket | null = null
  private audioCtx: AudioContext | null = null
  private streams: MediaStream[] = []
  private timer: number | null = null
  private floatBuf = new Float32Array(0)
  private startedAt = 0
  private pausedTotal = 0
  private pausedAt = 0
  onStopped: (meetingId: string) => void = () => {}

  // Milliseconds since the last audible frame ('quiet time').
  get quietMs(): number {
    if (this.state === 'idle' || !this.lastVoiceTs) return 0
    return Date.now() - this.lastVoiceTs
  }

  get elapsedMs(): number {
    if (!this.startedAt) return 0
    const pausedExtra = this.state === 'paused' ? Date.now() - this.pausedAt : 0
    return Date.now() - this.startedAt - this.pausedTotal - pausedExtra
  }

  async start(opts: {
    title: string
    language: string
    tabAudio: boolean
    participants?: string[]
    context?: string
    // Resume into an existing meeting, appending to its recording.
    resumeMeetingId?: string
    // Calendar occurrence this recording belongs to.
    calendarUid?: string
  }): Promise<string> {
    const streams: MediaStream[] = []
    const mic = await navigator.mediaDevices.getUserMedia({
      audio: { echoCancellation: true, noiseSuppression: true },
    })
    streams.push(mic)
    if (opts.tabAudio) {
      const tab = await navigator.mediaDevices.getDisplayMedia({ video: true, audio: true })
      if (tab.getAudioTracks().length) streams.push(tab)
      else tab.getTracks().forEach((t) => t.stop())
    }

    const proto = location.protocol === 'https:' ? 'wss://' : 'ws://'
    const ws = new WebSocket(proto + location.host + '/api/v1/live')
    ws.binaryType = 'arraybuffer'
    await new Promise<void>((res, rej) => {
      ws.onopen = () => res()
      ws.onerror = () => rej(new Error('无法连接服务器'))
    })
    ws.send(
      JSON.stringify({
        type: 'start',
        title: opts.title,
        language: opts.language,
        participants: opts.participants ?? [],
        context: opts.context ?? '',
        meetingId: opts.resumeMeetingId ?? '',
        calendarUid: opts.calendarUid ?? '',
      }),
    )
    const meetingId = await new Promise<string>((res, rej) => {
      const to = setTimeout(() => rej(new Error('服务器无响应')), 5000)
      ws.onmessage = (ev) => {
        const m = JSON.parse(ev.data)
        if (m.type === 'started') {
          clearTimeout(to)
          res(m.meetingId)
        } else if (m.type === 'error') {
          clearTimeout(to)
          rej(new Error(m.error))
        }
      }
    })
    ws.onmessage = (ev) => {
      const m = JSON.parse(ev.data)
      if (m.type === 'stopped') {
        ws.close()
        this.onStopped(m.meetingId)
      }
    }

    const audioCtx = new AudioContext()
    await audioCtx.audioWorklet.addModule(
      URL.createObjectURL(new Blob([workletCode], { type: 'application/javascript' })),
    )
    const node = new AudioWorkletNode(audioCtx, 'pcm-capture', { numberOfOutputs: 0 })
    for (const st of streams) {
      const tracks = st.getAudioTracks()
      if (tracks.length) audioCtx.createMediaStreamSource(new MediaStream(tracks)).connect(node)
    }
    node.port.onmessage = (e) => {
      const b = e.data as Float32Array
      // Cheap voice-activity check: any sample above the ambient floor.
      for (let i = 0; i < b.length; i += 16) {
        if (Math.abs(b[i]) > 0.02) {
          this.lastVoiceTs = Date.now()
          break
        }
      }
      const merged = new Float32Array(this.floatBuf.length + b.length)
      merged.set(this.floatBuf)
      merged.set(b, this.floatBuf.length)
      this.floatBuf = merged
    }
    const ratio = audioCtx.sampleRate / 16000
    this.timer = window.setInterval(() => {
      const outLen = Math.floor((this.floatBuf.length - 1) / ratio)
      if (outLen <= 0 || ws.readyState !== 1) return
      const out = new Int16Array(outLen)
      for (let i = 0; i < outLen; i++) {
        const pos = i * ratio
        const i0 = Math.floor(pos)
        const frac = pos - i0
        const v = this.floatBuf[i0] * (1 - frac) + (this.floatBuf[i0 + 1] ?? this.floatBuf[i0]) * frac
        out[i] = Math.max(-32768, Math.min(32767, Math.round(v * 32767)))
      }
      this.floatBuf = this.floatBuf.slice(Math.ceil(outLen * ratio))
      ws.send(out.buffer)
    }, 500)

    this.ws = ws
    this.audioCtx = audioCtx
    this.streams = streams
    this.meetingId = meetingId
    this.startedAt = Date.now()
    this.pausedTotal = 0
    this.lastVoiceTs = Date.now()
    this.state = 'recording'
    window.addEventListener('beforeunload', warnBeforeUnload)
    return meetingId
  }

  async pause() {
    if (this.state !== 'recording' || !this.audioCtx) return
    await this.audioCtx.suspend()
    this.pausedAt = Date.now()
    this.state = 'paused'
  }

  async resume() {
    if (this.state !== 'paused' || !this.audioCtx) return
    await this.audioCtx.resume()
    this.pausedTotal += Date.now() - this.pausedAt
    this.lastVoiceTs = Date.now() // don't count paused time as silence
    this.state = 'recording'
  }

  stop() {
    if (this.state === 'idle') return
    if (this.timer) clearInterval(this.timer)
    this.streams.forEach((st) => st.getTracks().forEach((t) => t.stop()))
    this.audioCtx?.close()
    if (this.ws && this.ws.readyState === 1) this.ws.send(JSON.stringify({ type: 'stop' }))
    this.state = 'idle'
    window.removeEventListener('beforeunload', warnBeforeUnload)
  }
}

export const recorder = new Recorder()
