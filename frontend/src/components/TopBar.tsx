// Global top bar: the old sidebar's functions folded into icons — record /
// import / history dropdown / settings. The workspace below gets the full
// width for the transcript | comments | summary three-pane layout.
import { FileText, History, Mic, Search, Settings, Trash2, Upload, Users, X } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { api, type Meeting } from '../api'
import { useApp } from '../App'
import Modal from './Modal'

const statusDot: Record<string, string> = {
  recording: 'bg-red-500 pulse-dot',
  processing: 'bg-amber-400 pulse-dot',
  failed: 'bg-red-300',
}

export default function TopBar() {
  const { meetings, refetchMeetings, openSettings, openImport } = useApp()
  const navigate = useNavigate()
  const location = useLocation()
  const [historyOpen, setHistoryOpen] = useState(false)
  const [search, setSearch] = useState('')
  const [deleting, setDeleting] = useState<Meeting | null>(null)
  const historyRef = useRef<HTMLDivElement>(null)

  const activeId = location.pathname.startsWith('/meeting/') ? location.pathname.split('/')[2] : null

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    if (!q) return meetings
    return meetings.filter((m) => m.title.toLowerCase().includes(q))
  }, [meetings, search])

  useEffect(() => {
    const h = (e: MouseEvent) => {
      if (historyRef.current && !historyRef.current.contains(e.target as Node)) setHistoryOpen(false)
    }
    document.addEventListener('mousedown', h)
    return () => document.removeEventListener('mousedown', h)
  }, [])

  const doDelete = async () => {
    if (!deleting) return
    await api(`/meetings/${deleting.id}`, { method: 'DELETE' })
    if (activeId === deleting.id) navigate('/')
    setDeleting(null)
    refetchMeetings()
  }

  return (
    <div className="flex h-12 shrink-0 items-center gap-2 border-b border-gray-200 bg-white px-4">
      <button onClick={() => navigate('/')} className="flex items-center gap-2" title="主页">
        <span className="flex h-7 w-7 items-center justify-center rounded-lg bg-blue-600 text-sm font-bold text-white">M</span>
        <span className="text-base font-semibold">Meeting Room</span>
      </button>

      <span className="ml-auto flex items-center gap-1">
        <button
          onClick={openImport}
          title="导入音频"
          className="flex h-8 w-8 items-center justify-center rounded-md text-gray-500 hover:bg-gray-100 hover:text-gray-700"
        >
          <Upload size={17} />
        </button>

        <div ref={historyRef} className="relative">
          <button
            onClick={() => setHistoryOpen(!historyOpen)}
            title="历史会议"
            className={`flex h-8 w-8 items-center justify-center rounded-md hover:bg-gray-100 ${historyOpen ? 'bg-gray-100 text-gray-700' : 'text-gray-500 hover:text-gray-700'}`}
          >
            <History size={17} />
          </button>
          {historyOpen && (
            <div className="absolute right-0 top-full z-30 mt-1 w-80 rounded-md border border-gray-200 bg-white shadow-lg">
              <div className="relative border-b border-gray-100 p-2">
                <Search size={14} className="absolute left-4 top-4 text-gray-400" />
                <input
                  autoFocus
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  placeholder="搜索会议..."
                  className="w-full rounded-md border border-gray-200 py-1.5 pl-8 pr-7 text-sm outline-none focus:border-blue-400"
                />
                {search && (
                  <button onClick={() => setSearch('')} className="absolute right-4 top-3.5 text-gray-400 hover:text-gray-600">
                    <X size={14} />
                  </button>
                )}
              </div>
              <div className="custom-scrollbar max-h-[60vh] overflow-y-auto p-1.5">
                {filtered.map((m) => (
                  <div
                    key={m.id}
                    onClick={() => {
                      navigate(`/meeting/${m.id}`)
                      setHistoryOpen(false)
                    }}
                    className={`group flex cursor-pointer items-center gap-2 rounded-md px-2 py-1.5 text-sm ${activeId === m.id ? 'bg-blue-100 text-blue-700' : 'text-gray-700 hover:bg-gray-100'}`}
                    title={m.title}
                  >
                    <span
                      className="relative flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-gray-100"
                      title={m.source === 'live' ? '实时录音' : '导入音频'}
                    >
                      {m.source === 'live' ? <Mic size={12} className="text-red-400" /> : <FileText size={12} className="text-gray-500" />}
                      {statusDot[m.status] && (
                        <span className={`absolute -right-0.5 -top-0.5 h-2 w-2 rounded-full ${statusDot[m.status]}`} />
                      )}
                    </span>
                    <span className="min-w-0 flex-1 truncate">{m.title}</span>
                    {/* Renaming lives on the meeting page's title, where the
                        content being named is actually visible. */}
                    <span className="hidden shrink-0 gap-0.5 group-hover:flex">
                      <button
                        onClick={(e) => {
                          e.stopPropagation()
                          setDeleting(m)
                        }}
                        className="rounded p-1 text-gray-400 hover:bg-red-100 hover:text-red-600"
                      >
                        <Trash2 size={12} />
                      </button>
                    </span>
                  </div>
                ))}
                {filtered.length === 0 && (
                  <div className="px-2 py-6 text-center text-xs text-gray-400">{search ? '没有匹配的会议' : '还没有会议记录'}</div>
                )}
              </div>
            </div>
          )}
        </div>

        <button
          onClick={() => navigate('/members')}
          title="成员管理(姓名、昵称、邮箱、声纹)"
          className={`flex h-8 w-8 items-center justify-center rounded-md hover:bg-gray-100 ${location.pathname === '/members' ? 'bg-gray-100 text-gray-700' : 'text-gray-500 hover:text-gray-700'}`}
        >
          <Users size={17} />
        </button>
        <button
          onClick={openSettings}
          title="设置"
          className="flex h-8 w-8 items-center justify-center rounded-md text-gray-500 hover:bg-gray-100 hover:text-gray-700"
        >
          <Settings size={17} />
        </button>
      </span>

      {deleting && (
        <Modal
          title="删除会议"
          onClose={() => setDeleting(null)}
          footer={
            <>
              <button onClick={() => setDeleting(null)} className="rounded-md px-4 py-2 text-sm text-gray-600 hover:bg-gray-100">
                取消
              </button>
              <button onClick={doDelete} className="rounded-md bg-red-600 px-4 py-2 text-sm text-white hover:bg-red-700">
                删除
              </button>
            </>
          }
        >
          <p className="text-sm text-gray-600">确认删除「{deleting.title}」?会议的录音和转录都会被删除,此操作不可恢复。</p>
        </Modal>
      )}
    </div>
  )
}
