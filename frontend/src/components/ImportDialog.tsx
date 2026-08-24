// Import audio — desktop ImportAudioDialog port: select file -> title /
// language -> upload -> live progress bar (WS job-progress) -> done.
import { AlertCircle, CheckCircle2, FileAudio, Loader2 } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, onWSMessage } from '../api'
import { useApp } from '../App'
import Modal from './Modal'

type Phase = 'select' | 'uploading' | 'progress' | 'error' | 'done'

export default function ImportDialog({ onClose }: { onClose: () => void }) {
  const navigate = useNavigate()
  const { refetchMeetings } = useApp()
  const [phase, setPhase] = useState<Phase>('select')
  const [file, setFile] = useState<File | null>(null)
  const [title, setTitle] = useState('')
  // '' = follow the default language in 设置 → 转录服务.
  const [language, setLanguage] = useState('')
  const [progress, setProgress] = useState(0)
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const meetingIdRef = useRef('')

  useEffect(
    () =>
      onWSMessage((msg) => {
        if (msg.type !== 'job-progress' || msg.meetingId !== meetingIdRef.current) return
        if (msg.status === 'running') {
          setPhase('progress')
          setProgress(msg.progress ?? 0)
          setMessage(msg.message ?? '')
        } else if (msg.status === 'completed') {
          setPhase('done')
          refetchMeetings()
        } else if (msg.status === 'failed') {
          setError(msg.message || '转录失败')
          setPhase('error')
        }
      }),
    [refetchMeetings],
  )

  const pick = (f: File | null) => {
    if (!f) return
    setFile(f)
    setTitle(f.name.replace(/\.[^.]+$/, ''))
  }

  const upload = async () => {
    if (!file) return
    setPhase('uploading')
    setError('')
    try {
      const fd = new FormData()
      fd.append('file', file)
      fd.append('title', title || file.name)
      fd.append('language', language)
      const r = await api<{ meeting: { id: string } }>('/meetings/import', { method: 'POST', body: fd })
      meetingIdRef.current = r.meeting.id
      setPhase('progress')
      setMessage('已入队,等待转录…')
      refetchMeetings()
    } catch (e: any) {
      setError(e.message)
      setPhase('error')
    }
  }

  const busy = phase === 'uploading' || phase === 'progress'

  return (
    <Modal
      title={
        phase === 'done' ? '导入完成' : phase === 'error' ? '导入失败' : phase === 'progress' ? '正在转录…' : '导入音频'
      }
      onClose={() => {
        if (!busy) onClose()
      }}
      footer={
        phase === 'select' ? (
          <>
            <button onClick={onClose} className="rounded-md px-4 py-2 text-sm text-gray-600 hover:bg-gray-100">
              取消
            </button>
            <button
              onClick={upload}
              disabled={!file}
              className="rounded-md bg-blue-600 px-4 py-2 text-sm text-white hover:bg-blue-700 disabled:opacity-50"
            >
              上传并转录
            </button>
          </>
        ) : phase === 'error' ? (
          <>
            <button onClick={onClose} className="rounded-md px-4 py-2 text-sm text-gray-600 hover:bg-gray-100">
              关闭
            </button>
            <button onClick={() => setPhase('select')} className="rounded-md bg-blue-600 px-4 py-2 text-sm text-white hover:bg-blue-700">
              重试
            </button>
          </>
        ) : phase === 'done' ? (
          <button
            onClick={() => {
              onClose()
              navigate(`/meeting/${meetingIdRef.current}`)
            }}
            className="rounded-md bg-blue-600 px-4 py-2 text-sm text-white hover:bg-blue-700"
          >
            查看会议
          </button>
        ) : (
          <span className="text-xs text-gray-400">转录在后台进行,关闭窗口不会中断</span>
        )
      }
    >
      {phase === 'select' && (
        <>
          <label className="flex cursor-pointer flex-col items-center gap-2 rounded-lg border-2 border-dashed border-gray-300 py-8 text-gray-500 hover:border-blue-400 hover:text-blue-600">
            <FileAudio size={28} />
            <span className="text-sm">{file ? file.name : '点击选择音频/视频文件'}</span>
            <span className="text-xs text-gray-400">支持 MP4, WAV, MP3, FLAC, OGG, M4A, WebM 等</span>
            <input type="file" accept="audio/*,video/*" className="hidden" onChange={(e) => pick(e.target.files?.[0] ?? null)} />
          </label>
          {file && (
            <div className="mt-4 space-y-3">
              <div>
                <label className="mb-1 block text-xs font-medium text-gray-500">会议标题</label>
                <input value={title} onChange={(e) => setTitle(e.target.value)} className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400" />
              </div>
              <div>
                <label className="mb-1 block text-xs font-medium text-gray-500">语言(默认跟随设置,可单次覆盖)</label>
                <select value={language} onChange={(e) => setLanguage(e.target.value)} className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none">
                  <option value="">默认(跟随设置)</option>
                  <option value="zh">中文</option>
                  <option value="en">English</option>
                  <option value="auto">自动检测</option>
                </select>
              </div>
            </div>
          )}
        </>
      )}

      {(phase === 'uploading' || phase === 'progress') && (
        <div className="py-2">
          <div className="mb-2 flex items-center justify-between text-sm text-gray-600">
            <span className="flex items-center gap-2">
              <Loader2 size={14} className="animate-spin" /> {phase === 'uploading' ? '上传中…' : '转录中'}
            </span>
            <span>{progress}%</span>
          </div>
          <div className="h-3 w-full overflow-hidden rounded-full bg-gray-100">
            <div className="h-full rounded-full bg-blue-600 transition-all" style={{ width: `${progress}%` }} />
          </div>
          {message && <p className="mt-2 text-center text-xs text-gray-500">{message}</p>}
        </div>
      )}

      {phase === 'error' && (
        <div className="flex items-start gap-2 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">
          <AlertCircle size={16} className="mt-0.5 shrink-0" /> {error}
        </div>
      )}

      {phase === 'done' && (
        <div className="flex flex-col items-center gap-2 py-4 text-gray-600">
          <CheckCircle2 size={32} className="text-green-500" />
          <p className="text-sm">转录完成,可以查看会议详情并生成总结了。</p>
        </div>
      )}
    </Modal>
  )
}
