import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { HashRouter, Route, Routes } from 'react-router-dom'
import { api, ensureWS, onWSMessage, type Meeting } from './api'
import TopBar from './components/TopBar'
import SettingsModal from './components/SettingsModal'
import ImportDialog from './components/ImportDialog'
import Modal from './components/Modal'
import RecordingView from './pages/RecordingView'
import MeetingDetails from './pages/MeetingDetails'
import MembersPage from './pages/MembersPage'
import { recorder } from './recorder'

type AppCtx = {
  meetings: Meeting[]
  refetchMeetings: () => Promise<void>
  openSettings: () => void
  openImport: () => void
}

const Ctx = createContext<AppCtx>(null as any)
export const useApp = () => useContext(Ctx)

export default function App() {
  const [meetings, setMeetings] = useState<Meeting[]>([])
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  // Forgot-to-stop detection: prompt when the room has gone quiet or the
  // transcript degenerates into sparse filler fragments.
  const [endPrompt, setEndPrompt] = useState(false)
  const [endCountdown, setEndCountdown] = useState(60)
  const recentSegsRef = useRef<Array<{ t: number; len: number }>>([])
  const snoozeTsRef = useRef(0)

  const refetchMeetings = useCallback(async () => {
    try {
      setMeetings(await api<Meeting[]>('/meetings'))
    } catch (e) {
      console.error(e)
    }
  }, [])

  useEffect(() => {
    ensureWS()
    refetchMeetings()
    return onWSMessage((msg) => {
      // Any job state transition can change list badges/titles.
      if (msg.type === 'job-progress' && msg.status !== 'running') refetchMeetings()
      // The first summary is started server-side, where the transcript
      // actually completes — one open page, ten or none, it runs once.
      // Track live segment sizes of the in-flight recording.
      if (msg.type === 'live-transcript' && msg.meetingId === recorder.meetingId && msg.segments) {
        const now = Date.now()
        recentSegsRef.current.push(...msg.segments.map((s) => ({ t: now, len: s.text.length })))
        recentSegsRef.current = recentSegsRef.current.slice(-50)
      }
    })
  }, [refetchMeetings])

  // Detector: every 10s while recording, look for (a) 5 minutes with no
  // audible input at all, or (b) the last >=8 segments all being <=6-char
  // fillers trickling in over >=4 minutes. Global — fires on any page.
  useEffect(() => {
    const t = setInterval(() => {
      if (recorder.state !== 'recording') {
        setEndPrompt(false)
        return
      }
      if (endPrompt || Date.now() - snoozeTsRef.current < 10 * 60 * 1000) return
      const quiet = recorder.quietMs > 5 * 60 * 1000
      const last8 = recentSegsRef.current.slice(-8)
      const fillers =
        last8.length >= 8 && last8.every((s) => s.len <= 6) && Date.now() - last8[0].t >= 4 * 60 * 1000
      if (quiet || fillers) {
        setEndCountdown(60)
        setEndPrompt(true)
      }
    }, 10000)
    return () => clearInterval(t)
  }, [endPrompt])

  // Countdown: unattended for 60s -> stop the recording automatically.
  useEffect(() => {
    if (!endPrompt) return
    const t = setInterval(() => {
      setEndCountdown((c) => {
        if (c <= 1) {
          setEndPrompt(false)
          recorder.stop()
          return 60
        }
        return c - 1
      })
    }, 1000)
    return () => clearInterval(t)
  }, [endPrompt])

  return (
    <Ctx.Provider
      value={{
        meetings,
        refetchMeetings,
        openSettings: () => setSettingsOpen(true),
        openImport: () => setImportOpen(true),
      }}
    >
      <HashRouter>
        <div className="flex h-screen flex-col overflow-hidden bg-gray-50 font-sans text-gray-900">
          <TopBar />
          <main className="relative flex min-w-0 min-h-0 flex-1 flex-col overflow-hidden">
            <Routes>
              <Route path="/" element={<RecordingView />} />
              <Route path="/meeting/:id" element={<MeetingDetails />} />
              <Route path="/members" element={<MembersPage />} />
            </Routes>
          </main>
        </div>
        {settingsOpen && <SettingsModal onClose={() => setSettingsOpen(false)} />}
        {importOpen && <ImportDialog onClose={() => setImportOpen(false)} />}
        {endPrompt && (
          <Modal
            title="会议似乎已经结束了?"
            onClose={() => {
              snoozeTsRef.current = Date.now()
              setEndPrompt(false)
            }}
            footer={
              <>
                <button
                  onClick={() => {
                    snoozeTsRef.current = Date.now()
                    setEndPrompt(false)
                  }}
                  className="rounded-md border border-gray-200 px-4 py-2 text-sm text-gray-600 hover:bg-gray-50"
                >
                  继续录音
                </button>
                <button
                  onClick={() => {
                    setEndPrompt(false)
                    recorder.stop()
                  }}
                  className="rounded-md bg-red-600 px-4 py-2 text-sm text-white hover:bg-red-700"
                >
                  结束录音
                </button>
              </>
            }
          >
            <p className="text-sm leading-relaxed text-gray-600">
              最近几分钟没有检测到有效的发言(长时间安静,或只有零星的短音)。如果会议已经结束,请结束录音;
              <span className="font-medium text-red-600">{endCountdown} 秒</span>
              内未操作将自动结束并保存。
            </p>
            <p className="mt-2 text-xs text-gray-400">点「继续录音」后 10 分钟内不会再提醒。</p>
          </Modal>
        )}
      </HashRouter>
    </Ctx.Provider>
  )
}
