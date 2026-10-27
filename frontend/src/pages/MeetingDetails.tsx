// Meeting workspace — three panes: transcript | comments | AI summary.
// Select text in the transcript or summary to attach a comment; commented
// spans get an amber underline (click for a popover), all comments collect
// in the middle pane, and regeneration folds them into the LLM prompt.
// The bug icon on the summary pane reveals exactly what was sent to the LLM.
import { AlertCircle, BookPlus, Bug, Check, ChevronDown, Copy, Download, FileText, Info, Loader2, MessageSquarePlus, MessagesSquare, Mic, Pencil, RefreshCw, Scissors, Send, Sparkles, Square, Strikethrough, Trash2, Upload, Wand2 } from 'lucide-react'
import { marked } from 'marked'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useParams } from 'react-router-dom'
import { api, fmtTs, onWSMessage, speakerColor, type Comment, type Job, type Meeting, type Segment, type Speaker, type Summary, type Template } from '../api'
import { useApp } from '../App'
import Modal from '../components/Modal'
import ParticipantPicker from '../components/ParticipantPicker'
import SpeakerEnrollDialog from '../components/SpeakerEnrollDialog'
import { recorder } from '../recorder'
import { applySummaryRevisions, normalizeBold } from '../summaryRevisions'

type SelInfo = { target: 'transcript' | 'summary'; quote: string; anchor: string; x: number; y: number }
type DraftKind = 'rewrite' | 'comment'

const REWRITE_MARK = 'cursor-pointer underline decoration-amber-400 decoration-2 underline-offset-2'
const COMMENT_MARK = 'cursor-pointer underline decoration-sky-400 decoration-2 underline-offset-2'
const DELETE_MARK = 'cursor-pointer line-through decoration-red-400 decoration-2 text-gray-400'
const markClass = (kind: string) =>
  kind === 'comment' ? COMMENT_MARK : kind === 'delete' ? DELETE_MARK : REWRITE_MARK
const markTitle = (kind: string) =>
  kind === 'comment' ? '有评论,点击查看' : kind === 'delete' ? '已标记删除:复制/下载与纪要生成时会去掉' : '已按改写显示,点击查看原文'

// The 0-based occurrence index of the selected quote within the container's
// rendered text — lets identical words in different places be annotated
// independently.
function occurrenceIndex(container: HTMLElement, range: Range, quote: string): number {
  const pre = document.createRange()
  pre.selectNodeContents(container)
  pre.setEnd(range.startContainer, range.startOffset)
  const offset = pre.toString().length
  const text = container.textContent ?? ''
  let n = 0
  let idx = text.indexOf(quote)
  while (idx !== -1 && idx < offset - 1) {
    n++
    idx = text.indexOf(quote, idx + 1)
  }
  return n
}

// Wrap the anchored occurrence of one comment's quote in the rendered
// summary. A quote confined to a single text node is wrapped exactly; one
// spanning inline markup (bold names, links) marks its whole block instead
// — otherwise deletions of a full bullet would show no strikethrough.
function wrapOccurrence(el: HTMLElement, c: Comment, want: number): boolean {
  let seen = 0
  const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT)
  let node: Text | null
  while ((node = walker.nextNode() as Text | null)) {
    if ((node.parentElement as HTMLElement | null)?.closest('u[data-cid]')) continue
    let from = 0
    while (true) {
      const i = node.data.indexOf(c.quote, from)
      if (i < 0) break
      if (seen === want) {
        const range = document.createRange()
        range.setStart(node, i)
        range.setEnd(node, i + c.quote.length)
        const u = document.createElement('u')
        u.className = markClass(c.kind)
        u.dataset.cid = c.id
        u.title = markTitle(c.kind)
        try {
          range.surroundContents(u)
          if (c.kind === 'rewrite') u.textContent = c.body
        } catch {}
        return true
      }
      seen++
      from = i + c.quote.length
    }
  }
  return false
}

// Fallback for quotes spanning inline markup: style the smallest block that
// contains the whole quote.
function markBlock(el: HTMLElement, c: Comment): boolean {
  const blocks = el.querySelectorAll<HTMLElement>('li, p, h1, h2, h3, h4, h5, h6, td, blockquote')
  let best: HTMLElement | null = null
  for (const b of blocks) {
    if (b.dataset.cid) continue
    if ((b.textContent ?? '').includes(c.quote) && (!best || b.contains(best) === false)) best = b
  }
  if (!best) return false
  best.dataset.cid = c.id
  best.title = markTitle(c.kind)
  best.className = `${best.className} ${markClass(c.kind)}`.trim()
  return true
}

function applySummaryMarks(el: HTMLElement, comments: Comment[]) {
  for (const c of comments) {
    if (c.target !== 'summary' || !c.quote) continue
    const want = c.anchor?.startsWith('occ:') ? parseInt(c.anchor.slice(4)) || 0 : 0
    if (wrapOccurrence(el, c, want)) continue
    if (want > 0 && wrapOccurrence(el, c, 0)) continue
    markBlock(el, c)
  }
}

export default function MeetingDetails() {
  const { id } = useParams<{ id: string }>()
  const { refetchMeetings } = useApp()
  const [meeting, setMeeting] = useState<Meeting | null>(null)
  const [job, setJob] = useState<Job | null>(null)
  const [segments, setSegments] = useState<Segment[]>([])
  const [summary, setSummary] = useState<Summary | null>(null)
  const [templates, setTemplates] = useState<Template[]>([])
  const [speakers, setSpeakers] = useState<Speaker[]>([])
  const [comments, setComments] = useState<Comment[]>([])
  const [expandedRevisionGroups, setExpandedRevisionGroups] = useState<Set<string>>(new Set())
  const [selectedTpl, setSelectedTpl] = useState('standard')
  const [tplOpen, setTplOpen] = useState(false)
  const [promptOpen, setPromptOpen] = useState(false)
  const [customPrompt, setCustomPrompt] = useState('')
  const [promptDraft, setPromptDraft] = useState('')
  const [copied, setCopied] = useState<'transcript' | 'summary' | null>(null)
  const [actionErr, setActionErr] = useState('')
  const [stopping, setStopping] = useState(false)
  const [labeling, setLabeling] = useState<Segment | null>(null)
  const [naming, setNaming] = useState(false)
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState('')
  // Escape closes the editor, which also fires blur; this tells the blur
  // handler the edit was abandoned rather than committed.
  const titleAbort = useRef(false)
  const [cancelling, setCancelling] = useState(false)
  const [nowTs, setNowTs] = useState(Date.now())
  const [ctxOpen, setCtxOpen] = useState(false)
  const [ctxText, setCtxText] = useState('')
  const [trimAt, setTrimAt] = useState<number | null>(null) // seconds, null = dialog closed
  const [trimming, setTrimming] = useState(false)
  // Recording resumed into THIS meeting from this page (accidental-stop fix).
  const [liveHere, setLiveHere] = useState(() => recorder.meetingId === id && recorder.state !== 'idle')
  const [resuming, setResuming] = useState(false)
  const [debugOpen, setDebugOpen] = useState(false)
  const [titleDebugOpen, setTitleDebugOpen] = useState(false)
  const [sDebugList, setSDebugList] = useState<Array<{ id: number; info: string; createdAt: string }>>([])
  const [sCancelling, setSCancelling] = useState(false)
  const [tDebugOpen, setTDebugOpen] = useState(false)
  const [tDebugList, setTDebugList] = useState<Array<{ id: number; info: string; createdAt: string }>>([])
  const [sel, setSel] = useState<SelInfo | null>(null)
  const [commentDraft, setCommentDraft] = useState<{ target: 'transcript' | 'summary'; kind: DraftKind; quote: string; anchor: string } | null>(null)
  const [commentBody, setCommentBody] = useState('')
  const [popover, setPopover] = useState<{ x: number; y: number; comment: Comment } | null>(null)
  const [popEdit, setPopEdit] = useState<string | null>(null)
  const [editing, setEditing] = useState<{ id: string; body: string } | null>(null)
  const [noteInput, setNoteInput] = useState('')
  const [flashSegId, setFlashSegId] = useState<number | null>(null)
  const [glossOpen, setGlossOpen] = useState(false)
  const [glossText, setGlossText] = useState('')
  const [glossDone, setGlossDone] = useState('')
  // Other exact occurrences of the text being rewritten, pre-checked so one
  // dialog fixes them all; the user can uncheck individual ones.
  type BatchMatch = { key: string; target: 'transcript' | 'summary'; anchor: string; before: string; after: string }
  const [batch, setBatch] = useState<BatchMatch[]>([])
  const [batchChecked, setBatchChecked] = useState<Set<string>>(new Set())
  const tplRef = useRef<HTMLDivElement>(null)
  const audioRef = useRef<HTMLAudioElement>(null)
  const articleRef = useRef<HTMLDivElement>(null)
  const [activeSegId, setActiveSegId] = useState<number | null>(null)
  // Playback auto-follow yields to the user: any manual interaction with
  // the transcript area (wheel / touch / press) pauses following for 5s.
  const userScrollTsRef = useRef(0)
  const noteUserScroll = () => {
    userScrollTsRef.current = Date.now()
  }

  const load = useCallback(async () => {
    if (!id) return
    try {
      const d = await api<{ meeting: Meeting; job?: Job }>(`/meetings/${id}`)
      setMeeting(d.meeting)
      setJob(d.job ?? null)
      setSegments(await api<Segment[]>(`/meetings/${id}/transcript`))
      setComments(await api<Comment[]>(`/meetings/${id}/comments`))
    } catch (e) {
      console.error(e)
    }
    try {
      const loadedSummary = await api<Summary>(`/meetings/${id}/summary`)
      setSummary(loadedSummary)
      setCustomPrompt(loadedSummary.customPrompt ?? '')
    } catch {
      setSummary(null)
    }
  }, [id])

  useEffect(() => {
    load()
    api<Template[]>('/summary-templates').then(setTemplates).catch(console.error)
    api<Speaker[]>('/speakers').then(setSpeakers).catch(console.error)
  }, [load])

  useEffect(
    () =>
      onWSMessage((msg) => {
        if (msg.meetingId !== id) return
        if (msg.type === 'summary-update') {
          setSCancelling(false)
          load()
        }
        if (msg.type === 'live-transcript') load()
        if (msg.type === 'meeting-updated') {
          load()
          refetchMeetings()
        }
        if (msg.type === 'job-progress') {
          if (msg.status === 'running') {
            // Summarize emits one running tick — pull the generating state
            // (the job may have been started globally, e.g. auto-summary).
            if (msg.jobType === 'summarize') load()
            else setJob((j) => (j ? { ...j, status: 'running', progress: msg.progress ?? j.progress } : j))
          } else {
            setCancelling(false)
            load()
          }
        }
      }),
    [id, load, refetchMeetings],
  )

  useEffect(() => {
    const h = (e: MouseEvent) => {
      if (tplRef.current && !tplRef.current.contains(e.target as Node)) setTplOpen(false)
      setPopover((p) => {
        if (!p) return p
        const t = e.target as HTMLElement
        return t.closest('#comment-popover') ? p : null
      })
    }
    document.addEventListener('mousedown', h)
    return () => document.removeEventListener('mousedown', h)
  }, [])

  // Render summary markdown into the ref'd article, then underline
  // commented spans (re-applied whenever content or comments change).
  useEffect(() => {
    const el = articleRef.current
    if (!el) return
    // Keep the previous version readable while a new one generates; only
    // the toolbar shows the in-progress state.
    if (summary?.content) {
      el.innerHTML = marked.parse(normalizeBold(summary.content)) as string
      applySummaryMarks(el, comments)
    } else {
      el.innerHTML = ''
    }
  }, [summary?.status, summary?.content, comments])

  const seekTo = (ms: number) => {
    const el = audioRef.current
    if (!el) return
    el.currentTime = ms / 1000
    el.play().catch(() => {})
  }

  const onAudioTime = () => {
    const el = audioRef.current
    if (!el || el.paused) return
    const t = el.currentTime * 1000
    let cur: Segment | null = null
    for (const s of segments) {
      if (s.startMs <= t) cur = s
      else break
    }
    setActiveSegId(cur ? cur.id : null)
  }

  useEffect(() => {
    if (activeSegId == null) return
    if (Date.now() - userScrollTsRef.current < 5000) return // user is reading elsewhere
    document.getElementById(`seg-${activeSegId}`)?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  }, [activeSegId])

  const generate = async () => {
    setActionErr('')
    try {
      await api(`/meetings/${id}/summarize`, {
        method: 'POST',
        body: JSON.stringify({ template: selectedTpl, customPrompt }),
      })
      // Keep the previous content readable while regenerating; the timer
      // starts from now, not from the last generation's timestamp.
      setSCancelling(false)
      setSummary((s) => ({
        ...(s ?? ({} as Summary)),
        meetingId: id!,
        template: selectedTpl,
        customPrompt,
        status: 'generating',
        updatedAt: new Date().toISOString(),
      }))
    } catch (e: any) {
      setActionErr(e.message)
    }
  }

  const saveCustomPrompt = async (value = promptDraft) => {
    const next = value.trim()
    try {
      await api(`/meetings/${id}/summary-prompt`, { method: 'PUT', body: JSON.stringify({ template: selectedTpl, customPrompt: next }) })
      setCustomPrompt(next)
      setPromptOpen(false)
    } catch (e: any) {
      setActionErr(e.message)
    }
  }

  const retranscribe = async () => {
    setActionErr('')
    try {
      await api(`/meetings/${id}/retranscribe`, { method: 'POST' })
      setJob({ id: '', meetingId: id!, type: 'retranscribe', status: 'running', progress: 0, startedAt: new Date().toISOString() } as Job)
      setCancelling(false)
      refetchMeetings()
    } catch (e: any) {
      setActionErr(e.message)
    }
  }

  const cancelTranscribe = async () => {
    setCancelling(true)
    try {
      await api(`/meetings/${id}/transcribe/cancel`, { method: 'POST' })
      // The worker aborts within a couple of seconds and pushes the final
      // job-progress over WS, which reloads this view.
    } catch (e: any) {
      setActionErr(e.message)
      setCancelling(false)
    }
  }

  const resumeRecording = async () => {
    if (recorder.state !== 'idle') {
      setActionErr('已有录音在进行中')
      return
    }
    setResuming(true)
    setActionErr('')
    try {
      recorder.onStopped = () => {
        setLiveHere(false)
        load()
        refetchMeetings()
      }
      await recorder.start({ title: meeting?.title ?? '', language: '', tabAudio: false, resumeMeetingId: id })
      setLiveHere(true)
      refetchMeetings()
    } catch (e: any) {
      setActionErr('续录失败:' + e.message)
    }
    setResuming(false)
  }

  // A meeting stays "recording" until its live WebSocket closes cleanly, so a
  // crash or a restart strands it there — and while stranded it cannot be
  // summarized, truncated or resumed. Only a person can say the recording is
  // really over: the server has no way to tell a dead session from a live one
  // it does not own.
  const stopStranded = async () => {
    setStopping(true)
    setActionErr('')
    try {
      const r = await api<{ status: string; untranscribedTailMs?: number }>(
        `/meetings/${id}/recording/stop`,
        { method: 'POST' },
      )
      if (r.status === 'failed') setActionErr('这场录制没有录到音频,已标记为失败')
      load()
      refetchMeetings()
    } catch (e: any) {
      setActionErr('标记停止失败:' + e.message)
    }
    setStopping(false)
  }

  const doTruncate = async () => {
    if (trimAt == null) return
    setTrimming(true)
    setActionErr('')
    try {
      await api(`/meetings/${id}/audio/truncate`, { method: 'POST', body: JSON.stringify({ seconds: trimAt }) })
      setTrimAt(null)
      await load()
      refetchMeetings()
    } catch (e: any) {
      setActionErr(e.message)
    }
    setTrimming(false)
  }

  const saveContext = async () => {
    try {
      await api(`/meetings/${id}/context`, { method: 'PUT', body: JSON.stringify({ context: ctxText.trim() }) })
      setCtxOpen(false)
      await load()
    } catch (e: any) {
      setActionErr(e.message)
    }
  }

  const openTranscribeDebug = async () => {
    try {
      setTDebugList(await api(`/meetings/${id}/transcribe-debug`))
      setTDebugOpen(true)
    } catch (e: any) {
      setActionErr(e.message)
    }
  }

  const openSummaryDebug = async () => {
    try {
      setSDebugList(await api(`/meetings/${id}/summary-debug`))
      setDebugOpen(true)
    } catch (e: any) {
      setActionErr(e.message)
    }
  }

  const openTitleDebug = async () => {
    try {
      setSDebugList(await api(`/meetings/${id}/summary-debug`))
      setTitleDebugOpen(true)
    } catch (e: any) {
      setActionErr(e.message)
    }
  }

  const cancelSummarize = async () => {
    setSCancelling(true)
    try {
      await api(`/meetings/${id}/summarize/cancel`, { method: 'POST' })
    } catch (e: any) {
      setActionErr(e.message)
      setSCancelling(false)
    }
  }

  // Renaming happens in place on the title itself. Saving marks the meeting
  // title_customized, so later summaries stop auto-renaming it; the Wand
  // button hands it back to the AI.
  const saveTitle = async () => {
    const next = titleDraft.trim()
    setEditingTitle(false)
    if (titleAbort.current || !meeting || !next || next === meeting.title) return
    try {
      await api(`/meetings/${id}`, { method: 'PATCH', body: JSON.stringify({ title: next }) })
      await load()
      refetchMeetings()
    } catch (e: any) {
      setActionErr('重命名失败:' + e.message)
    }
  }

  const startEditTitle = () => {
    if (!meeting) return
    titleAbort.current = false // clear a leftover Escape that skipped blur
    setTitleDraft(meeting.title)
    setEditingTitle(true)
  }

  const autoName = async () => {
    setNaming(true)
    setActionErr('')
    try {
      await api(`/meetings/${id}/autoname`, { method: 'POST' })
      await load()
      refetchMeetings()
    } catch (e: any) {
      setActionErr('AI 命名失败:' + e.message)
    }
    setNaming(false)
  }

  const toggleParticipant = async (name: string) => {
    if (!meeting) return
    const cur = meeting.participants ?? []
    const next = cur.includes(name) ? cur.filter((n) => n !== name) : [...cur, name]
    setMeeting({ ...meeting, participants: next })
    try {
      await api(`/meetings/${id}/participants`, { method: 'PUT', body: JSON.stringify({ participants: next }) })
    } catch (e: any) {
      setActionErr(e.message)
    }
  }

  const onSelect = (target: 'transcript' | 'summary') => () => {
    setTimeout(() => {
      const s = window.getSelection()
      const text = s?.toString().trim() ?? ''
      if (!text) {
        setSel(null)
        return
      }
      const range = s!.getRangeAt(0)
      const quote = text.slice(0, 300)
      // Pin the selection to a position so identical text elsewhere is not
      // confused with it.
      let anchor = ''
      if (target === 'transcript') {
        const startEl = range.startContainer instanceof Element ? range.startContainer : range.startContainer.parentElement
        const segEl = startEl?.closest('[id^="seg-"]')
        if (segEl) anchor = 'seg:' + segEl.id.slice(4)
      } else if (articleRef.current) {
        anchor = 'occ:' + occurrenceIndex(articleRef.current, range, quote)
      }
      const rect = range.getBoundingClientRect()
      setSel({ target, quote, anchor, x: rect.left + rect.width / 2, y: rect.top })
    }, 0)
  }

  // Find every other exact occurrence of the quote across transcript and
  // rendered summary (skipping spots already rewritten), with sentence
  // context for the confirmation list.
  const findBatchMatches = (quote: string, originTarget: string, originAnchor: string): BatchMatch[] => {
    const items: BatchMatch[] = []
    const ctx = (text: string, idx: number) => ({
      before: (idx > 25 ? '…' : '') + text.slice(Math.max(0, idx - 25), idx),
      after: text.slice(idx + quote.length, idx + quote.length + 25) + (idx + quote.length + 25 < text.length ? '…' : ''),
    })
    for (const s of segments) {
      const idx = s.text.indexOf(quote)
      if (idx < 0) continue
      const anchor = `seg:${s.id}`
      if (originTarget === 'transcript' && anchor === originAnchor) continue
      if (comments.some((c) => c.kind === 'rewrite' && c.quote === quote && c.anchor === anchor)) continue
      items.push({ key: anchor, target: 'transcript', anchor, ...ctx(s.text, idx) })
    }
    const text = articleRef.current?.textContent ?? ''
    let idx = text.indexOf(quote)
    let n = 0
    while (idx !== -1 && items.length < 30) {
      const anchor = `occ:${n}`
      const dup = comments.some((c) => c.kind === 'rewrite' && c.target === 'summary' && c.quote === quote && c.anchor === anchor)
      if (!(originTarget === 'summary' && anchor === originAnchor) && !dup) {
        items.push({ key: 's-' + anchor, target: 'summary', anchor, ...ctx(text, idx) })
      }
      n++
      idx = text.indexOf(quote, idx + quote.length)
    }
    return items
  }

  const saveComment = async () => {
    if (!commentDraft || !commentBody.trim()) return
    if (commentDraft.kind === 'rewrite' && commentBody.trim() === commentDraft.quote) {
      setActionErr('改写内容与原文相同')
      return
    }
    try {
      const revisionGroupId = commentDraft.kind === 'rewrite' && batchChecked.size > 0 ? crypto.randomUUID() : ''
      await api(`/meetings/${id}/comments`, {
        method: 'POST',
        body: JSON.stringify({ ...commentDraft, body: commentBody.trim(), revisionGroupId }),
      })
      // Batch: same rewrite for every other checked occurrence.
      for (const m of batch.filter((b) => batchChecked.has(b.key))) {
        await api(`/meetings/${id}/comments`, {
          method: 'POST',
          body: JSON.stringify({ target: m.target, kind: 'rewrite', quote: commentDraft.quote, body: commentBody.trim(), anchor: m.anchor, revisionGroupId }),
        })
      }
      setCommentDraft(null)
      setCommentBody('')
      setBatch([])
      setComments(await api<Comment[]>(`/meetings/${id}/comments`))
    } catch (e: any) {
      setActionErr(e.message)
    }
  }

  const deleteComment = async (c: Comment) => {
    try {
      await api(`/meetings/${id}/comments/${c.id}`, { method: 'DELETE' })
      setComments((prev) => prev.filter((x) => x.id !== c.id))
      setPopover(null)
    } catch (e: any) {
      setActionErr(e.message)
    }
  }

  const updateComment = async (cid: string, body: string) => {
    if (!body.trim()) return
    try {
      const c = await api<Comment>(`/meetings/${id}/comments/${cid}`, { method: 'PUT', body: JSON.stringify({ body: body.trim() }) })
      setComments((prev) => prev.map((x) => (x.id === cid ? c : x)))
      setEditing(null)
      setPopover(null)
      setPopEdit(null)
    } catch (e: any) {
      setActionErr(e.message)
    }
  }

  // Free-standing note from the comments pane input (no quoted text).
  const addNote = async () => {
    if (!noteInput.trim()) return
    try {
      await api(`/meetings/${id}/comments`, {
        method: 'POST',
        body: JSON.stringify({ target: 'general', quote: '', body: noteInput.trim() }),
      })
      setNoteInput('')
      setComments(await api<Comment[]>(`/meetings/${id}/comments`))
    } catch (e: any) {
      setActionErr(e.message)
    }
  }

  // Turn rewrite comments (term corrections) into structured glossary
  // entries: one line per correct term, all its observed mis-transcriptions
  // merged into that line — `词(常被误转录为:错1、错2)`.
  const parseGlossLine = (line: string): { term: string; wrongs: string[] } | null => {
    const i = line.lastIndexOf('(常被误转录为:')
    if (i < 0 || !line.endsWith(')')) return null
    const term = line.slice(0, i).trim()
    const wrongs = line
      .slice(i + '(常被误转录为:'.length, -1)
      .split('、')
      .map((w) => w.trim())
      .filter(Boolean)
    return term ? { term, wrongs } : null
  }

  const glossLine = (term: string, wrongs: string[]) =>
    wrongs.length > 0 ? `${term}(常被误转录为:${wrongs.join('、')})` : term

  const openGlossary = () => {
    // Merge this meeting's rewrites by correct term before showing.
    const byTerm = new Map<string, Set<string>>()
    for (const c of comments) {
      if (c.kind !== 'rewrite' || !c.quote || c.target === 'general') continue
      const term = c.body.trim()
      const wrong = c.quote.length > 40 ? c.quote.slice(0, 40) + '…' : c.quote.trim()
      if (!term || term === wrong) continue
      if (!byTerm.has(term)) byTerm.set(term, new Set())
      byTerm.get(term)!.add(wrong)
    }
    setGlossText([...byTerm].map(([t, ws]) => glossLine(t, [...ws])).join('\n'))
    setGlossDone('')
    setGlossOpen(true)
  }

  const saveGlossary = async () => {
    try {
      const s = await api<{ context?: { background: string; glossary: string } }>('/settings')
      const existingLines = (s.context?.glossary ?? '').split('\n').map((l) => l.trim()).filter(Boolean)
      // Index existing structured entries; free-text lines stay untouched.
      const termAt = new Map<string, number>()
      const wrongsAt = new Map<number, Set<string>>()
      existingLines.forEach((l, i) => {
        const p = parseGlossLine(l)
        if (p) {
          termAt.set(p.term, i)
          wrongsAt.set(i, new Set(p.wrongs))
        }
      })
      let added = 0
      let mergedInto = 0
      for (const raw of glossText.split('\n')) {
        const line = raw.trim()
        if (!line) continue
        const p = parseGlossLine(line) ?? { term: line, wrongs: [] }
        const at = termAt.get(p.term)
        if (at !== undefined) {
          const set = wrongsAt.get(at)!
          const before = set.size
          p.wrongs.forEach((w) => w !== p.term && set.add(w))
          if (set.size > before) {
            existingLines[at] = glossLine(p.term, [...set])
            mergedInto++
          }
        } else {
          existingLines.push(glossLine(p.term, p.wrongs))
          termAt.set(p.term, existingLines.length - 1)
          wrongsAt.set(existingLines.length - 1, new Set(p.wrongs))
          added++
        }
      }
      if (added === 0 && mergedInto === 0) {
        setGlossDone('没有新内容(词条与误识别形式都已在词表中)')
        return
      }
      await api('/settings/context', {
        method: 'PUT',
        body: JSON.stringify({ background: s.context?.background ?? '', glossary: existingLines.join('\n') }),
      })
      setGlossDone(`✅ 新增 ${added} 个词条,合并 ${mergedInto} 个词条的误识别形式;词表可在 设置 → 背景词表 查看`)
    } catch (e: any) {
      setGlossDone('❌ ' + e.message)
    }
  }

  // Click a comment card -> scroll its target into view (centered) and
  // flash a one-off highlight.
  const locateComment = (c: Comment) => {
    if (c.target === 'summary') {
      const el = articleRef.current?.querySelector(`u[data-cid="${c.id}"]`) as HTMLElement | null
      if (!el) return
      el.scrollIntoView({ block: 'center', behavior: 'smooth' })
      el.classList.add('bg-amber-200', 'transition-colors', 'duration-700')
      setTimeout(() => el.classList.remove('bg-amber-200'), 1600)
    } else if (c.target === 'transcript' && c.quote) {
      let seg: Segment | undefined
      if (c.anchor.startsWith('seg:')) {
        const segId = parseInt(c.anchor.slice(4))
        seg = segments.find((s) => s.id === segId)
      }
      seg ??= segments.find((s) => s.text.includes(c.quote) || (c.quote.length > 8 && c.quote.includes(s.text)))
      if (!seg) return
      document.getElementById(`seg-${seg.id}`)?.scrollIntoView({ block: 'center', behavior: 'smooth' })
      setFlashSegId(seg.id)
      setTimeout(() => setFlashSegId(null), 1600)
    }
  }

  // Copies carry the rewrites: what you see (corrected) is what you paste.
  const appliedSegText = (s: Segment): string => {
    let text = s.text
    for (const c of comments) {
      if (c.target !== 'transcript' || !c.quote) continue
      if (c.kind !== 'rewrite' && c.kind !== 'delete') continue
      const applies = c.anchor.startsWith('seg:') ? c.anchor === `seg:${s.id}` : text.includes(c.quote)
      if (applies) text = text.replace(c.quote, c.kind === 'delete' ? '' : c.body)
    }
    return text.trim()
  }

  const appliedSummary = (): { text: string; unapplied: Comment[] } => {
    const { text, unapplied } = applySummaryRevisions(summary?.content ?? '', comments)
    return { text: normalizeBold(text), unapplied }
  }

  // Revisions that could not be placed would otherwise vanish without a trace:
  // the file just comes out missing an edit the page shows as made.
  const reportUnapplied = (unapplied: Comment[]) => {
    if (unapplied.length === 0) {
      setActionErr('')
      return
    }
    setActionErr(
      `有 ${unapplied.length} 条修订相互重叠或无法定位，已停止复制和下载。请先处理这些修订后重试:` +
        unapplied.map((c) => `「${c.quote.replace(/\s+/g, ' ').slice(0, 20)}」`).join('、'),
    )
  }

  // Download the summary as a Markdown file (rewrites applied, same as copy).
  const downloadSummary = () => {
    const { text, unapplied } = appliedSummary()
    reportUnapplied(unapplied)
    if (unapplied.length > 0) return
    const blob = new Blob([text], { type: 'text/markdown;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `${(meeting?.title ?? '会议纪要').replace(/[\\/:*?"<>|]/g, '_')}.md`
    a.click()
    URL.revokeObjectURL(url)
  }

  const copy = (kind: 'transcript' | 'summary') => {
    let text: string
    if (kind === 'transcript') {
      text = segments.map((s) => `[${fmtTs(s.startMs)}] ${s.speaker ? s.speaker + ': ' : ''}${appliedSegText(s)}`).join('\n')
    } else {
      const applied = appliedSummary()
      reportUnapplied(applied.unapplied)
      if (applied.unapplied.length > 0) return
      text = applied.text
    }
    navigator.clipboard.writeText(text)
    setCopied(kind)
    setTimeout(() => setCopied(null), 1500)
  }

  // Underline the parts of a segment's text covered by transcript comments.
  // Anchored comments only light up their own segment; legacy un-anchored
  // ones fall back to text matching.
  const renderSegText = (s: Segment) => {
    const hits = comments.filter((c) => {
      if (c.target !== 'transcript' || !c.quote) return false
      if (c.anchor.startsWith('seg:')) return c.anchor === `seg:${s.id}`
      return s.text.includes(c.quote) || (c.quote.length > 8 && c.quote.includes(s.text))
    })
    if (hits.length === 0) return s.text

    const openPop = (c: Comment) => (e: React.MouseEvent) => {
      e.stopPropagation()
      setPopEdit(null)
      setPopover({ x: e.clientX, y: e.clientY, comment: c })
    }

    // Multi-segment selection: the whole line sits inside one longer quote;
    // underline it as-is (in-place substitution only works 1:1).
    if (hits.length === 1 && s.text.indexOf(hits[0].quote) < 0) {
      const c = hits[0]
      return (
        <u className={markClass(c.kind)} title={markTitle(c.kind)} onClick={openPop(c)}>
          {s.text}
        </u>
      )
    }

    // Apply ALL revisions on this line: split the text into plain pieces
    // and marks, each comment consuming one occurrence of its quote
    // (first-come-first-served on overlaps; unmatched ones are skipped).
    const pieces: Array<string | Comment> = [s.text]
    for (const c of hits) {
      for (let i = 0; i < pieces.length; i++) {
        const p = pieces[i]
        if (typeof p !== 'string') continue
        const idx = p.indexOf(c.quote)
        if (idx < 0) continue
        pieces.splice(i, 1, p.slice(0, idx), c, p.slice(idx + c.quote.length))
        break
      }
    }
    return (
      <>
        {pieces.map((p, i) =>
          typeof p === 'string' ? (
            <span key={i}>{p}</span>
          ) : (
            <u key={i} className={markClass(p.kind)} title={markTitle(p.kind)} onClick={openPop(p)}>
              {p.kind === 'rewrite' ? p.body : p.quote}
            </u>
          ),
        )}
      </>
    )
  }

  const onSummaryClick = (e: React.MouseEvent) => {
    const mark = (e.target as HTMLElement).closest('[data-cid]') as HTMLElement | null
    if (!mark) return
    const c = comments.find((x) => x.id === mark.dataset.cid)
    if (c) {
      setPopEdit(null)
      setPopover({ x: e.clientX, y: e.clientY, comment: c })
    }
  }

  const transcribing = job && job.status === 'running' && job.type !== 'summarize'
  // A finished-but-failed transcribe job: the spinner simply disappears,
  // so without this the click looks like it did nothing. worker.go stores
  // the reason on the job and emits it with the final job-progress.
  const failedJob = job && job.status === 'failed' && job.type !== 'summarize' ? job : null
  // Cancel shares the failed status (ErrCancelled); it is a user action, not a fault.
  const transcribeCancelled = failedJob?.error === '已取消'
  const generating = summary?.status === 'generating'
  // While recording finalizes or transcription runs, the transcript is
  // still growing — generating now would miss the ending.
  const transcriptSettling = meeting?.status === 'recording' || meeting?.status === 'processing' || !!transcribing

  // Live elapsed / ETA readout while transcription or summarization runs.
  useEffect(() => {
    if (!transcribing && !generating) return
    const t = setInterval(() => setNowTs(Date.now()), 1000)
    return () => clearInterval(t)
  }, [transcribing, generating])

  const jobTiming = (() => {
    if (!transcribing || !job?.startedAt) return null
    const elapsed = Math.max(0, nowTs - new Date(job.startedAt).getTime())
    let text = `已用 ${fmtTs(elapsed)}`
    // Chunk progress runs 10→95; extrapolate the remainder from the pace.
    const p = job.progress
    if (p > 12 && p < 95) {
      const workDone = (p - 10) / 85
      const remaining = (elapsed * (1 - workDone)) / workDone
      text += ` · 预计还需 ${fmtTs(remaining)}`
    }
    return text
  })()

  // Summary generation elapsed (StartSummary stamps updatedAt; the LLM
  // gives no progress signal, so no ETA).
  const genTiming =
    generating && summary?.updatedAt ? `已用 ${fmtTs(Math.max(0, nowTs - new Date(summary.updatedAt).getTime()))}` : null
  const tplName = templates.find((t) => t.id === (summary?.template ?? selectedTpl))?.name

  if (!meeting) return <div className="flex h-full items-center justify-center text-gray-400">加载中…</div>

  const revisionGroups = new Map<string, Comment[]>()
  for (const comment of comments) {
    if (comment.kind !== 'rewrite' || !comment.revisionGroupId) continue
    revisionGroups.set(comment.revisionGroupId, [...(revisionGroups.get(comment.revisionGroupId) ?? []), comment])
  }

  return (
    <div className="flex h-full flex-col">
      {/* Title bar */}
      <div className="flex items-center gap-2 border-b border-gray-200 bg-white px-4 py-2.5">
        <span
          className="flex shrink-0 items-center gap-1 rounded-full bg-gray-100 px-2 py-0.5 text-[11px] text-gray-500"
          title={meeting.source === 'live' ? '来源:实时录音' : '来源:导入音频'}
        >
          {meeting.source === 'live' ? <Mic size={11} className="text-red-400" /> : <Upload size={11} />}
          {meeting.source === 'live' ? '实时' : '导入'}
        </span>
        {editingTitle ? (
          <input
            autoFocus
            value={titleDraft}
            onChange={(e) => setTitleDraft(e.target.value)}
            onBlur={saveTitle}
            onKeyDown={(e) => {
              if (e.key === 'Enter') saveTitle()
              if (e.key === 'Escape') {
                titleAbort.current = true
                setEditingTitle(false)
              }
            }}
            className="min-w-0 flex-1 rounded border border-blue-400 bg-white px-1.5 py-0.5 text-base font-semibold outline-none"
          />
        ) : (
          <h1 className="flex min-w-0 flex-1 items-center gap-1 text-base font-semibold">
            <span className="truncate">{meeting.title}</span>
            <button
              onClick={startEditTitle}
              title="修改标题"
              className="shrink-0 rounded p-1 text-gray-300 hover:bg-gray-100 hover:text-gray-600"
            >
              <Pencil size={12} />
            </button>
          </h1>
        )}
        <button
          onClick={() => {
            setCtxText(meeting.context ?? '')
            setCtxOpen(true)
          }}
          title={meeting.context ? '查看/编辑会议背景信息' : '添加会议背景信息(日历邀请、议程等)'}
          className={`shrink-0 rounded p-1.5 hover:bg-gray-100 ${meeting.context ? 'text-blue-500 hover:text-blue-600' : 'text-gray-400 hover:text-gray-600'}`}
        >
          <Info size={15} />
        </button>
        <button
          onClick={autoName}
          disabled={naming || segments.length === 0}
          title="AI 根据会议内容重新命名"
          className="shrink-0 rounded p-1.5 text-gray-400 hover:bg-gray-100 hover:text-blue-600 disabled:opacity-40"
        >
          {naming ? <Loader2 size={15} className="animate-spin" /> : <Wand2 size={15} />}
        </button>
        <button
          onClick={openTitleDebug}
          title="查看 AI 命名的提示词与历史记录"
          className="shrink-0 rounded p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600"
        >
          <Bug size={13} />
        </button>
        <span className="shrink-0 text-xs text-gray-400">
          {meeting.durationSeconds ? `${Math.round(meeting.durationSeconds / 60)} 分钟 · ` : ''}
          {new Date(meeting.createdAt).toLocaleString()}
        </span>
      </div>

      {actionErr && <div className="border-b border-red-200 bg-red-50 px-4 py-2 text-sm text-red-700">{actionErr}</div>}

      {meeting.status === 'recording' && !liveHere && (
        <div className="flex items-center gap-3 border-b border-amber-200 bg-amber-50 px-4 py-2 text-sm text-amber-800">
          <AlertCircle size={16} className="shrink-0" />
          <span className="min-w-0 flex-1">
            这场会议还停在「录制中」,但本机没有在录它 —— 多半是上次录制没有正常结束。
            标记停止会保存已录到的音频并补转录尾巴;在此之前无法生成纪要或续录。
          </span>
          <button
            onClick={stopStranded}
            disabled={stopping}
            className="shrink-0 rounded bg-amber-600 px-2.5 py-1 text-xs font-medium text-white hover:bg-amber-700 disabled:opacity-40"
          >
            {stopping ? <Loader2 size={13} className="animate-spin" /> : '标记录制结束'}
          </button>
        </div>
      )}

      <div className="flex min-h-0 flex-1 overflow-hidden">
        {/* LEFT: transcript */}
        <div className="flex w-full min-w-0 shrink-0 flex-col border-r border-gray-200 bg-white md:w-[34%]">
          <div className="flex items-center gap-2 border-b border-gray-100 px-3 py-2">
            <span className="text-sm font-medium text-gray-700">转录</span>
            {transcribing && (
              <span className="flex items-center gap-1 text-xs text-amber-600">
                <Loader2 size={12} className="animate-spin" /> 转录中 {job?.progress ?? 0}%
                {jobTiming && <span className="text-gray-400">· {jobTiming}</span>}
              </span>
            )}
            {failedJob && (
              <span className={`flex items-center gap-1 text-xs ${transcribeCancelled ? 'text-gray-400' : 'text-red-600'}`}>
                {transcribeCancelled ? <Square size={11} /> : <AlertCircle size={12} />}
                {transcribeCancelled ? '已取消' : '转录失败'}
              </span>
            )}
            <span className="ml-auto flex gap-1">
              <button onClick={openTranscribeDebug} title="查看每次转录请求的接口与参数" className="rounded p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600">
                <Bug size={14} />
              </button>
              <button onClick={() => copy('transcript')} title="复制转录" className="rounded p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600">
                {copied === 'transcript' ? <Check size={14} className="text-green-600" /> : <Copy size={14} />}
              </button>
              {transcribing ? (
                <button
                  onClick={cancelTranscribe}
                  disabled={cancelling}
                  title="中止转录"
                  className="rounded p-1.5 text-red-500 hover:bg-red-50 hover:text-red-600 disabled:opacity-40"
                >
                  {cancelling ? <Loader2 size={14} className="animate-spin" /> : <Square size={14} />}
                </button>
              ) : (
                <button onClick={retranscribe} title="重新转录" className="rounded p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600">
                  <RefreshCw size={14} />
                </button>
              )}
            </span>
          </div>

          {failedJob && !transcribeCancelled && (
            <div className="border-b border-red-200 bg-red-50 px-3 py-2 text-xs text-red-700">
              <p className="mb-1 font-medium">转录失败,转录内容保持不变</p>
              <p className="break-words">{failedJob.error || '未知错误'}</p>
              <p className="mt-1 text-red-500">修正后可点右上角 ⟳ 重新转录。</p>
            </div>
          )}

          {speakers.length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5 border-b border-gray-100 px-3 py-2">
              <span className="text-xs text-gray-400" title="选中的人的声纹会随本会议的转录请求携带(本地最多 16 人);不选则说话人显示为 A/B 等原始标签">
                参会人
              </span>
              <ParticipantPicker
                speakers={speakers}
                selected={meeting.participants ?? []}
                onToggle={toggleParticipant}
                compact
              />
            </div>
          )}

          <div
            className="custom-scrollbar min-h-0 flex-1 overflow-y-auto px-3 py-2"
            onMouseUp={onSelect('transcript')}
            onWheel={noteUserScroll}
            onTouchMove={noteUserScroll}
            onMouseDown={noteUserScroll}
          >
            {segments.length === 0 && (
              <div className="mt-10 text-center text-sm text-gray-400">{transcribing ? '转录进行中…' : '暂无转录内容'}</div>
            )}
            {segments.map((s) => (
              <div
                key={s.id}
                id={`seg-${s.id}`}
                className={`mb-1 flex gap-2 rounded-md px-1 py-0.5 transition-colors duration-700 ${flashSegId === s.id ? 'bg-amber-100' : activeSegId === s.id ? 'bg-blue-50 ring-1 ring-inset ring-blue-200' : ''}`}
              >
                <button
                  onClick={() => seekTo(s.startMs)}
                  disabled={!meeting.hasAudio && meeting.status !== 'recording'}
                  title="播放这句的原始录音"
                  className={`min-w-[42px] pt-0.5 text-left text-xs enabled:hover:text-blue-600 enabled:hover:underline ${activeSegId === s.id ? 'font-semibold text-blue-600' : 'text-gray-400'}`}
                >
                  {activeSegId === s.id ? '▶ ' + fmtTs(s.startMs) : fmtTs(s.startMs)}
                </button>
                {s.speaker && (
                  <button
                    onClick={() => setLabeling(s)}
                    title="标注这句话的说话人(登记声纹)"
                    className="w-[52px] shrink-0 break-all rounded pt-0.5 text-left text-xs font-semibold leading-tight hover:underline"
                    style={{ color: speakerColor(s.speaker) }}
                  >
                    {s.speaker}
                  </button>
                )}
                <span className="text-sm leading-relaxed text-gray-800">{renderSegText(s)}</span>
              </div>
            ))}
          </div>

          {(meeting.hasAudio || meeting.status === 'recording') && (
            <div className="flex items-center gap-1 border-t border-gray-100 px-3 py-1.5">
              <audio
                ref={audioRef}
                controls
                preload="none"
                src={`/api/v1/meetings/${id}/audio?v=${meeting.durationSeconds ?? 0}`}
                onTimeUpdate={onAudioTime}
                onEnded={() => setActiveSegId(null)}
                className="h-8 min-w-0 flex-1"
              />
              {liveHere ? (
                <span className="flex shrink-0 items-center gap-1.5 pl-1">
                  <span className="pulse-dot h-2.5 w-2.5 rounded-full bg-red-500" />
                  <span className="text-xs font-medium text-red-600">续录中</span>
                  <button
                    onClick={() => recorder.stop()}
                    title="停止录音"
                    className="rounded p-1.5 text-red-500 hover:bg-red-50 hover:text-red-600"
                  >
                    <Square size={14} />
                  </button>
                </span>
              ) : (
                meeting.status !== 'recording' &&
                meeting.status !== 'processing' && (
                  <>
                    <button
                      onClick={() => {
                        const t = audioRef.current?.currentTime ?? 0
                        if (t >= 1) setTrimAt(t)
                        else setActionErr('请先把播放器定位到要保留的结束位置(至少 1 秒)')
                      }}
                      title="在当前播放位置截断:删除其后的音频与转录(误录太长时使用)"
                      className="shrink-0 rounded p-1.5 text-gray-400 hover:bg-red-50 hover:text-red-600"
                    >
                      <Scissors size={14} />
                    </button>
                    {meeting.source === 'live' && (
                      <button
                        onClick={resumeRecording}
                        disabled={resuming}
                        title="继续录制:在这段录音末尾接着录(意外停止后使用)"
                        className="shrink-0 rounded p-1.5 text-gray-400 hover:bg-red-50 hover:text-red-600 disabled:opacity-40"
                      >
                        {resuming ? <Loader2 size={14} className="animate-spin" /> : <Mic size={14} />}
                      </button>
                    )}
                  </>
                )
              )}
            </div>
          )}
        </div>

        {/* RIGHT (order-last): revisions — full height for whole-meeting review */}
        <div className="order-last hidden w-72 shrink-0 flex-col border-l border-gray-200 bg-white lg:flex xl:w-80">
            <div className="flex items-center gap-1.5 border-b border-gray-100 px-3 py-1.5">
              <MessagesSquare size={14} className="text-gray-400" />
              <span className="text-sm font-medium text-gray-700">修订</span>
              {comments.length > 0 && <span className="text-xs text-gray-400">({comments.length})</span>}
              <span className="ml-auto text-[11px] text-gray-300">划选转录/纪要文本可改写或评论</span>
              <button
                onClick={openGlossary}
                disabled={!comments.some((c) => c.kind === 'rewrite' && c.quote && c.target !== 'general')}
                title="把「改写」类修订一键写入专属名词表(评论不会写入)"
                className="rounded p-1 text-gray-400 hover:bg-gray-100 hover:text-blue-600 disabled:opacity-30"
              >
                <BookPlus size={14} />
              </button>
            </div>
            <div className="custom-scrollbar min-h-0 flex-1 overflow-y-auto p-2">
              {comments.length === 0 && (
                <div className="mt-6 px-3 text-center text-xs leading-relaxed text-gray-400">
                  <MessageSquarePlus size={20} className="mx-auto mb-1.5 text-gray-300" />
                  划选转录或纪要文本可「改写」(纠正原文)或「评论」;两者都会在生成纪要时作为上下文。
                </div>
              )}
              {comments.map((c) => {
                const members = c.revisionGroupId ? revisionGroups.get(c.revisionGroupId) : undefined
                const isGroupLeader = !!members && members[0].id === c.id
                const expanded = !!members && expandedRevisionGroups.has(c.revisionGroupId)
                if (members && !isGroupLeader && !expanded) return null
                return (
                  <div key={c.id}>
                    {members && isGroupLeader && (
                      <button
                        onClick={() => setExpandedRevisionGroups((prev) => {
                          const next = new Set(prev)
                          expanded ? next.delete(c.revisionGroupId) : next.add(c.revisionGroupId)
                          return next
                        })}
                        aria-expanded={expanded}
                        className={`relative mb-2 w-full text-left text-xs ${expanded ? '' : 'h-[76px]'}`}
                      >
                        {!expanded && members.length > 2 && <span aria-hidden className="absolute inset-x-2 bottom-0 h-[62px] rounded-md border border-amber-100 bg-amber-50/70" />}
                        {!expanded && members.length > 1 && <span aria-hidden className="absolute inset-x-1 bottom-1 h-[66px] rounded-md border border-amber-200 bg-amber-50/90" />}
                        <span className={`relative flex w-full items-center gap-2 rounded-md border border-amber-300 bg-white px-2.5 text-amber-700 shadow-sm transition hover:-translate-y-0.5 hover:border-amber-400 ${expanded ? 'py-2' : 'min-h-[66px] py-2'}`}>
                          <ChevronDown size={14} className={`shrink-0 transition-transform ${expanded ? '' : '-rotate-90'}`} />
                          <span className="font-medium">改写</span>
                          <span className="ml-auto rounded-full bg-amber-100 px-1.5 py-0.5 font-medium text-amber-700">×{members.length}</span>
                          {!expanded && <span className="absolute bottom-2 left-8 right-12 truncate text-[11px] text-amber-600">{members[0].quote} → {members[0].body}</span>}
                          {expanded && <span className="min-w-0 flex-1 truncate text-amber-600">展开 {members.length} 条修订</span>}
                        </span>
                      </button>
                    )}
                    {(!members || expanded) && <div
                  onClick={() => locateComment(c)}
                  className={`group mb-2 rounded-md border border-gray-100 bg-gray-50 px-2.5 py-2 ${c.quote ? 'cursor-pointer hover:border-amber-300' : ''}`}
                  title={c.quote ? '点击定位到对应的内容' : undefined}
                >
                  <div className="mb-1 flex items-center gap-1.5">
                    <span
                      className={`rounded px-1 py-0.5 text-[10px] ${c.kind === 'rewrite' ? 'bg-amber-50 text-amber-600' : c.kind === 'comment' ? 'bg-sky-50 text-sky-600' : c.kind === 'delete' ? 'bg-red-50 text-red-600' : 'bg-gray-100 text-gray-500'}`}
                    >
                      {c.kind === 'rewrite' ? '改写' : c.kind === 'comment' ? '评论' : c.kind === 'delete' ? '删除' : '备注'}
                    </span>
                    {c.quote && (
                      <span className="rounded bg-gray-100 px-1 py-0.5 text-[10px] text-gray-400">{c.target === 'summary' ? '纪要' : '转录'}</span>
                    )}
                    <span className="text-[10px] font-medium text-gray-500">{c.author || '参会者'}</span>
                    <span className="text-[10px] text-gray-300">{new Date(c.createdAt).toLocaleString()}</span>
                    <span className="ml-auto flex gap-0.5 opacity-0 group-hover:opacity-100">
                      <button
                        onClick={(e) => {
                          e.stopPropagation()
                          setEditing({ id: c.id, body: c.body })
                        }}
                        className="rounded p-0.5 text-gray-300 hover:bg-gray-200 hover:text-gray-600"
                      >
                        <Pencil size={12} />
                      </button>
                      <button
                        onClick={(e) => {
                          e.stopPropagation()
                          deleteComment(c)
                        }}
                        className="rounded p-0.5 text-gray-300 hover:bg-gray-200 hover:text-red-500"
                      >
                        <Trash2 size={12} />
                      </button>
                    </span>
                  </div>
                  {c.quote && (
                    <div className="mb-1 border-l-2 border-amber-300 pl-2 text-[11px] leading-snug text-gray-400">
                      {c.quote.length > 60 ? c.quote.slice(0, 60) + '…' : c.quote}
                    </div>
                  )}
                  {editing?.id === c.id ? (
                    <div onClick={(e) => e.stopPropagation()}>
                      <textarea
                        autoFocus
                        value={editing.body}
                        onChange={(e) => setEditing({ id: c.id, body: e.target.value })}
                        rows={2}
                        className="w-full resize-none rounded border border-blue-300 px-2 py-1 text-xs outline-none"
                      />
                      <div className="mt-1 flex justify-end gap-1">
                        <button onClick={() => setEditing(null)} className="rounded px-2 py-0.5 text-[11px] text-gray-500 hover:bg-gray-200">
                          取消
                        </button>
                        <button onClick={() => updateComment(c.id, editing.body)} className="rounded bg-blue-600 px-2 py-0.5 text-[11px] text-white hover:bg-blue-700">
                          保存
                        </button>
                      </div>
                    </div>
                  ) : (
                    <div className="text-xs leading-relaxed text-gray-700">{c.body}</div>
                  )}
                    </div>}
                  </div>
                )
              })}
            </div>
            <div className="flex items-end gap-1.5 border-t border-gray-100 p-2">
              <textarea
                value={noteInput}
                onChange={(e) => setNoteInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && !e.shiftKey) {
                    e.preventDefault()
                    addNote()
                  }
                }}
                placeholder="直接写备注(无需划选),Enter 发送…"
                rows={1}
                className="max-h-20 min-h-[30px] flex-1 resize-none rounded-md border border-gray-200 px-2 py-1.5 text-xs outline-none focus:border-blue-400"
              />
              <button
                onClick={addNote}
                disabled={!noteInput.trim()}
                title="添加备注"
                className="flex h-[30px] w-8 shrink-0 items-center justify-center rounded-md bg-blue-600 text-white hover:bg-blue-700 disabled:opacity-40"
              >
                <Send size={13} />
              </button>
            </div>
          </div>

        {/* MIDDLE: summary */}
        <div className="flex min-w-0 flex-1 flex-col bg-white">
          <div className="flex items-center gap-2 border-b border-gray-100 px-3 py-2">
            <div ref={tplRef} className="relative">
              <button
                onClick={() => setTplOpen(!tplOpen)}
                className="flex items-center gap-1.5 rounded-md border border-gray-200 px-3 py-1.5 text-sm text-gray-600 hover:bg-gray-50"
              >
                <FileText size={14} /> {templates.find((t) => t.id === selectedTpl)?.name ?? '模板'} <ChevronDown size={14} />
              </button>
              {tplOpen && (
                <div className="absolute left-0 top-full z-20 mt-1 w-64 rounded-md border border-gray-200 bg-white py-1 shadow-lg">
                  {templates.map((t) => (
                    <button
                      key={t.id}
                      title={t.description}
                      onClick={() => {
                        setSelectedTpl(t.id)
                        setTplOpen(false)
                      }}
                      className="flex w-full items-center justify-between px-3 py-2 text-left text-sm hover:bg-gray-50"
                    >
                      <span>
                        <span className="block">{t.name}</span>
                        <span className="block text-xs text-gray-400">{t.description}</span>
                      </span>
                      {selectedTpl === t.id && <Check size={14} className="shrink-0 text-green-600" />}
                    </button>
                  ))}
                </div>
              )}
            </div>
            <button
              onClick={() => {
                setPromptDraft(customPrompt)
                setPromptOpen(true)
              }}
              title={customPrompt ? '编辑当前会议的补充提示词' : '添加当前会议的补充提示词'}
              className={`rounded-md border p-1.5 transition ${customPrompt ? 'border-blue-200 bg-blue-50 text-blue-600 hover:bg-blue-100' : 'border-transparent text-gray-400 hover:bg-gray-100 hover:text-gray-700'}`}
            >
              <Pencil size={15} />
            </button>

            <span className="ml-auto flex items-center gap-1">
              <button onClick={openSummaryDebug} title="查看每次生成的提示词、参数与耗时" className="rounded p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600">
                <Bug size={14} />
              </button>
              {generating ? (
                <>
                  <span className="flex items-center gap-1.5 px-2 text-xs text-gray-500">
                    <Loader2 size={13} className="animate-spin text-blue-600" /> 生成中…
                    {genTiming && <span className="text-gray-400">{genTiming}</span>}
                  </span>
                  <button
                    onClick={cancelSummarize}
                    disabled={sCancelling}
                    title="中止生成"
                    className="rounded p-1.5 text-red-500 hover:bg-red-50 hover:text-red-600 disabled:opacity-40"
                  >
                    {sCancelling ? <Loader2 size={14} className="animate-spin" /> : <Square size={14} />}
                  </button>
                </>
              ) : summary?.status === 'completed' ? (
                <>
                  <button onClick={() => copy('summary')} title="复制纪要" className="rounded p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600">
                    {copied === 'summary' ? <Check size={14} className="text-green-600" /> : <Copy size={14} />}
                  </button>
                  <button onClick={downloadSummary} title="下载纪要(.md)" className="rounded p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600">
                    <Download size={14} />
                  </button>
                  <button
                    onClick={generate}
                    disabled={segments.length === 0 || transcriptSettling}
                    title={transcriptSettling ? '转录尚未完成(收尾/转录中),现在生成会缺失结尾内容' : '重新生成(会带上批注和背景词表)'}
                    className="rounded p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600 disabled:opacity-40"
                  >
                    <RefreshCw size={14} />
                  </button>
                </>
              ) : (
                <button
                  onClick={generate}
                  disabled={segments.length === 0 || transcriptSettling}
                  title={transcriptSettling ? '转录尚未完成(收尾/转录中),现在生成会缺失结尾内容' : undefined}
                  className="flex items-center gap-1.5 rounded-md bg-blue-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
                >
                  <Sparkles size={14} /> {transcriptSettling ? '待转录完成' : '生成总结'}
                </button>
              )}
            </span>
          </div>

          <div className="custom-scrollbar min-h-0 flex-1 overflow-y-auto" onMouseUp={onSelect('summary')}>
            {generating && !summary?.content && (
              <div className="mt-24 flex flex-col items-center gap-3 text-gray-500">
                <Loader2 size={28} className="animate-spin text-blue-600" />
                <p className="text-sm">
                  正在生成 AI 总结{tplName ? `(${tplName})` : ''}…本地模型可能需要几分钟
                  {genTiming && <span className="ml-1 text-gray-400">({genTiming})</span>}
                </p>
              </div>
            )}
            {!generating && summary?.status === 'failed' && (
              <div className="mx-auto mt-6 max-w-lg rounded-md border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">
                总结生成失败:{summary.error}
                {summary.content ? '(下方保留了上一次的生成结果)' : ''}
              </div>
            )}
            {!generating && !summary && (
              <div className="mt-28 text-center text-gray-400">
                <Sparkles size={32} className="mx-auto mb-3 text-gray-300" />
                <p className="font-medium text-gray-500">还没有 AI 总结</p>
                <p className="mt-1 text-sm">选择一个模板,点击"生成总结"</p>
              </div>
            )}
            <div
              ref={articleRef}
              onClick={onSummaryClick}
              className={`prose prose-sm mx-auto max-w-3xl px-8 py-6 prose-headings:font-semibold prose-table:text-xs ${summary?.content ? '' : 'hidden'}`}
            />
          </div>
        </div>
      </div>

      {/* Floating actions at the text selection: rewrite or comment */}
      {sel && (
        <div
          style={{ left: sel.x, top: sel.y - 6 }}
          className="fixed z-40 flex -translate-x-1/2 -translate-y-full overflow-hidden rounded-md bg-gray-900 text-xs text-white shadow-lg"
        >
          <button
            onMouseDown={(e) => {
              e.preventDefault()
              const matches = findBatchMatches(sel.quote, sel.target, sel.anchor)
              setBatch(matches)
              setBatchChecked(new Set(matches.map((m) => m.key)))
              setCommentDraft({ target: sel.target, kind: 'rewrite', quote: sel.quote, anchor: sel.anchor })
              setCommentBody(sel.quote)
              setSel(null)
            }}
            className="flex items-center gap-1 px-2.5 py-1.5 hover:bg-gray-700"
          >
            <Pencil size={12} /> 改写
          </button>
          <span className="my-1 w-px bg-gray-700" />
          <button
            onMouseDown={(e) => {
              e.preventDefault()
              setBatch([])
              setCommentDraft({ target: sel.target, kind: 'comment', quote: sel.quote, anchor: sel.anchor })
              setCommentBody('')
              setSel(null)
            }}
            className="flex items-center gap-1 px-2.5 py-1.5 hover:bg-gray-700"
          >
            <MessageSquarePlus size={13} /> 评论
          </button>
          <span className="my-1 w-px bg-gray-700" />
          <button
            onMouseDown={(e) => {
              e.preventDefault()
              const s = sel
              setSel(null)
              api(`/meetings/${id}/comments`, {
                method: 'POST',
                body: JSON.stringify({ target: s.target, kind: 'delete', quote: s.quote, anchor: s.anchor, body: '删除此内容' }),
              })
                .then(async () => setComments(await api<Comment[]>(`/meetings/${id}/comments`)))
                .catch((e: any) => setActionErr(e.message))
            }}
            title="标记删除:页面上划掉,复制/下载与生成纪要时去掉"
            className="flex items-center gap-1 px-2.5 py-1.5 hover:bg-gray-700"
          >
            <Strikethrough size={13} /> 删除
          </button>
        </div>
      )}

      {/* Comment popover on underlined spans */}
      {popover && (
        <div
          id="comment-popover"
          style={{ left: Math.min(popover.x, window.innerWidth - 280), top: popover.y + 10 }}
          className="fixed z-40 w-64 rounded-md border border-gray-200 bg-white p-3 shadow-xl"
        >
          <div className="mb-1 flex items-center gap-1.5">
            <span className="text-[11px] font-medium text-gray-500">{popover.comment.author || '参会者'}</span>
            <span className="text-[10px] text-gray-300">{new Date(popover.comment.createdAt).toLocaleString()}</span>
          </div>
          {popover.comment.quote && (
            <div className={`mb-1.5 border-l-2 pl-2 text-[11px] leading-snug text-gray-400 ${popover.comment.kind === 'comment' ? 'border-sky-300' : 'border-amber-300'}`}>
              <span className="mr-1 rounded bg-gray-100 px-1 text-[10px] text-gray-400">{popover.comment.kind === 'comment' ? '引用' : '原文'}</span>
              {popover.comment.quote.length > 80 ? popover.comment.quote.slice(0, 80) + '…' : popover.comment.quote}
            </div>
          )}
          {popEdit != null ? (
            <>
              <textarea
                autoFocus
                value={popEdit}
                onChange={(e) => setPopEdit(e.target.value)}
                rows={3}
                className="w-full resize-none rounded border border-blue-300 px-2 py-1 text-xs outline-none"
              />
              <div className="mt-1.5 flex justify-end gap-1">
                <button onClick={() => setPopEdit(null)} className="rounded px-2 py-0.5 text-[11px] text-gray-500 hover:bg-gray-100">
                  取消
                </button>
                <button onClick={() => updateComment(popover.comment.id, popEdit)} className="rounded bg-blue-600 px-2 py-0.5 text-[11px] text-white hover:bg-blue-700">
                  保存
                </button>
              </div>
            </>
          ) : (
            <>
              <div className="text-sm leading-relaxed text-gray-700">{popover.comment.body}</div>
              <div className="mt-2 flex items-center justify-end gap-1">
                <button onClick={() => setPopEdit(popover.comment.body)} className="flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-gray-400 hover:bg-gray-100 hover:text-gray-600">
                  <Pencil size={11} /> 编辑
                </button>
                <button onClick={() => deleteComment(popover.comment)} className="flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-gray-400 hover:bg-red-50 hover:text-red-500">
                  <Trash2 size={11} /> 删除
                </button>
              </div>
            </>
          )}
        </div>
      )}

      {trimAt != null && (
        <Modal
          title="截断录音"
          onClose={() => setTrimAt(null)}
          footer={
            <>
              <button onClick={() => setTrimAt(null)} className="rounded-md px-4 py-2 text-sm text-gray-600 hover:bg-gray-100">
                取消
              </button>
              <button
                onClick={doTruncate}
                disabled={trimming}
                className="flex items-center gap-1.5 rounded-md bg-red-600 px-4 py-2 text-sm text-white hover:bg-red-700 disabled:opacity-50"
              >
                {trimming && <Loader2 size={14} className="animate-spin" />} 确认截断
              </button>
            </>
          }
        >
          <p className="text-sm leading-relaxed text-gray-600">
            将把录音在 <span className="font-semibold text-gray-900">{fmtTs(trimAt * 1000)}</span> 处截断:
          </p>
          <ul className="mt-2 list-disc pl-5 text-sm leading-relaxed text-gray-600">
            <li>
              保留前 {fmtTs(trimAt * 1000)},删除其后约{' '}
              {meeting.durationSeconds ? fmtTs(Math.max(0, meeting.durationSeconds - trimAt) * 1000) : '?'} 的音频
            </li>
            <li>删除截断点之后的所有转录段落</li>
            <li>纪要不会自动变化,建议之后点「重新生成」</li>
          </ul>
          <p className="mt-2 text-xs text-gray-400">原始完整音频会在服务器上保留一份 .bak 备份,误操作可找回。</p>
        </Modal>
      )}

      {promptOpen && (
        <Modal
          title="补充提示词"
          onClose={() => setPromptOpen(false)}
          footer={
            <>
              <button onClick={() => { setPromptDraft(''); void saveCustomPrompt('') }} className="rounded-md px-3 py-2 text-sm text-gray-500 hover:bg-gray-100">
                清空
              </button>
              <button onClick={() => setPromptOpen(false)} className="rounded-md px-3 py-2 text-sm text-gray-600 hover:bg-gray-100">
                取消
              </button>
              <button onClick={() => void saveCustomPrompt()} className="rounded-md bg-blue-600 px-4 py-2 text-sm text-white hover:bg-blue-700">
                保存
              </button>
            </>
          }
        >
          <p className="mb-3 text-sm text-gray-500">此要求仅用于当前会议，会追加在所选纪要模板之后，并在下次生成时生效。</p>
          <textarea
            autoFocus
            value={promptDraft}
            onChange={(e) => setPromptDraft(e.target.value)}
            placeholder="例如：请突出风险项，并在末尾列出待办负责人"
            rows={6}
            className="w-full resize-y rounded-md border border-gray-200 px-3 py-2 text-sm outline-none focus:border-blue-400"
          />
        </Modal>
      )}

      {ctxOpen && (
        <Modal
          title="会议背景信息"
          onClose={() => setCtxOpen(false)}
          wide
          footer={
            <>
              <button onClick={() => setCtxOpen(false)} className="rounded-md px-4 py-2 text-sm text-gray-600 hover:bg-gray-100">
                取消
              </button>
              <button onClick={saveContext} className="rounded-md bg-blue-600 px-4 py-2 text-sm text-white hover:bg-blue-700">
                保存
              </button>
            </>
          }
        >
          <textarea
            autoFocus
            value={ctxText}
            onChange={(e) => setCtxText(e.target.value)}
            placeholder={'可直接粘贴日历邀请、议程、参会人名单、相关链接等,例如:\nAI UI Automation Weekly Meeting\nThursday, July 30 4:00 – 5:00pm\n1. version note  2. badcase review  3. new idea proposal'}
            rows={10}
            className="w-full resize-y rounded-md border border-gray-300 px-3 py-2 text-sm leading-relaxed outline-none focus:border-blue-400"
          />
          <p className="mt-2 text-xs text-gray-400">背景信息会在生成会议纪要和 AI 命名时作为上下文,帮助识别议程、参会人与专有名词;保存后立即生效于下一次生成。</p>
        </Modal>
      )}

      {glossOpen && (
        <Modal
          title="批注写入专属名词表"
          onClose={() => setGlossOpen(false)}
          footer={
            <>
              <button onClick={() => setGlossOpen(false)} className="rounded-md px-4 py-2 text-sm text-gray-600 hover:bg-gray-100">
                {glossDone.startsWith('✅') ? '关闭' : '取消'}
              </button>
              {!glossDone.startsWith('✅') && (
                <button onClick={saveGlossary} disabled={!glossText.trim()} className="rounded-md bg-blue-600 px-4 py-2 text-sm text-white hover:bg-blue-700 disabled:opacity-50">
                  写入词表
                </button>
              )}
            </>
          }
        >
          <p className="mb-2 text-xs leading-relaxed text-gray-500">
            以下条目根据本会议的批注生成(格式:正确词(常被误转录为:原文)),可先编辑再写入。写入后对所有会议生效:转录请求会携带词表做术语优先,生成纪要时也会按词表纠正。已在词表中的条目会自动跳过。
          </p>
          <textarea
            value={glossText}
            onChange={(e) => setGlossText(e.target.value)}
            rows={8}
            className="w-full resize-y rounded-md border border-gray-300 px-3 py-2 font-mono text-xs leading-relaxed outline-none focus:border-blue-400"
          />
          {glossDone && <p className="mt-2 text-sm text-gray-600">{glossDone}</p>}
        </Modal>
      )}

      {commentDraft && (
        <Modal
          title={commentDraft.kind === 'rewrite' ? '改写原文' : '添加评论'}
          onClose={() => setCommentDraft(null)}
          footer={
            <>
              <button onClick={() => setCommentDraft(null)} className="rounded-md px-4 py-2 text-sm text-gray-600 hover:bg-gray-100">
                取消
              </button>
              <button onClick={saveComment} disabled={!commentBody.trim()} className="rounded-md bg-blue-600 px-4 py-2 text-sm text-white hover:bg-blue-700 disabled:opacity-50">
                {commentDraft.kind === 'rewrite'
                  ? `保存改写${batchChecked.size > 0 && batch.length > 0 ? `(共 ${1 + [...batchChecked].filter((k) => batch.some((b) => b.key === k)).length} 处)` : ''}`
                  : '保存评论'}
              </button>
            </>
          }
        >
          <div className="mb-3 rounded-md bg-gray-50 px-3 py-2 text-xs text-gray-500">
            <span className="mr-1 rounded bg-gray-200 px-1 text-[10px] text-gray-500">原文</span>“{commentDraft.quote}”
          </div>
          <textarea
            autoFocus
            value={commentBody}
            onChange={(e) => setCommentBody(e.target.value)}
            placeholder={
              commentDraft.kind === 'rewrite'
                ? '直接修改成正确的内容,例如把“SPUAT”改成“SPA UT”…'
                : '写下你的看法或补充,例如:此决定由王工负责跟进…'
            }
            rows={3}
            className="w-full resize-none rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400"
          />
          {commentDraft.kind === 'rewrite' && batch.length > 0 && (
            <div className="mt-3">
              <div className="mb-1 flex items-center text-xs font-medium text-gray-500">
                转录/纪要中还有 {batch.length} 处相同内容,已选中一并改写(可取消)
                <button
                  onClick={() =>
                    setBatchChecked((prev) => (prev.size === batch.length ? new Set() : new Set(batch.map((m) => m.key))))
                  }
                  className="ml-auto text-[11px] text-blue-500 hover:underline"
                >
                  {batchChecked.size === batch.length ? '全不选' : '全选'}
                </button>
              </div>
              <div className="custom-scrollbar max-h-40 overflow-y-auto rounded-md border border-gray-200">
                {batch.map((m) => (
                  <label key={m.key} className="flex cursor-pointer items-start gap-2 border-b border-gray-100 px-2.5 py-1.5 text-xs last:border-b-0 hover:bg-gray-50">
                    <input
                      type="checkbox"
                      checked={batchChecked.has(m.key)}
                      onChange={(e) =>
                        setBatchChecked((prev) => {
                          const next = new Set(prev)
                          e.target.checked ? next.add(m.key) : next.delete(m.key)
                          return next
                        })
                      }
                      className="mt-0.5 shrink-0"
                    />
                    <span className={`mt-0.5 shrink-0 rounded px-1 py-0.5 text-[10px] ${m.target === 'summary' ? 'bg-blue-50 text-blue-500' : 'bg-amber-50 text-amber-600'}`}>
                      {m.target === 'summary' ? '纪要' : '转录'}
                    </span>
                    <span className="min-w-0 leading-relaxed text-gray-600">
                      {m.before}
                      <b className="text-gray-900 underline decoration-amber-400 decoration-2">{commentDraft.quote}</b>
                      {m.after}
                    </span>
                  </label>
                ))}
              </div>
            </div>
          )}
          <p className="mt-2 text-xs text-gray-400">
            {commentDraft.kind === 'rewrite'
              ? '改写会直接在页面上替换显示(下划线标识,浮层可看原文),生成纪要时按改写执行,并可一键沉淀到专属名词表。'
              : '评论不改变原文显示(蓝色下划线标识),生成纪要时作为参考上下文,不会写入名词表。'}
          </p>
        </Modal>
      )}

      {tDebugOpen && (
        <Modal
          title="转录调试:每次请求的接口与参数"
          onClose={() => setTDebugOpen(false)}
          wide
          footer={
            <>
              <button onClick={openTranscribeDebug} className="flex items-center gap-1.5 rounded-md border border-gray-200 px-4 py-2 text-sm text-gray-600 hover:bg-gray-50">
                <RefreshCw size={14} /> 刷新
              </button>
              <button onClick={() => setTDebugOpen(false)} className="rounded-md bg-gray-100 px-4 py-2 text-sm text-gray-700 hover:bg-gray-200">
                关闭
              </button>
            </>
          }
        >
          {tDebugList.length === 0 && <p className="py-8 text-center text-sm text-gray-400">还没有转录请求记录(本次功能上线后的转录才会记录)</p>}
          {tDebugList.map((d) => {
            let p: any = null
            try {
              p = JSON.parse(d.info)
            } catch {}
            if (!p) return null
            // Job-level total: prominent summary card.
            if (p.kind === 'batch-total') {
              const cancelled = p.error === '已取消'
              return (
                <div key={d.id} className="mb-2 rounded-md border border-blue-200 bg-blue-50/40 px-3 py-2 text-xs">
                  <div className="flex items-center gap-2 font-medium text-gray-700">
                    <span>
                      转录任务{p.error ? (cancelled ? ' ⏹ 已取消' : ' ❌ 失败') : ' ✅ 完成'} · 总耗时{' '}
                      <span className="text-blue-600">{((p.elapsedMs ?? 0) / 1000).toFixed(1)}s</span> · 共 {p.requests} 次请求
                    </span>
                    <span className="ml-auto font-normal text-gray-400">{new Date(d.createdAt).toLocaleString()}</span>
                  </div>
                  <p className="mt-0.5 text-gray-500">
                    {p.model} · {p.url}
                    {p.error && !cancelled ? ` · ${p.error}` : ''}
                  </p>
                </div>
              )
            }
            const label = `${p.kind === 'live' ? `实时 · 窗口#${p.windowIdx}` : `批量 · 请求#${p.request}`} · ${p.error ? '❌ 失败' : `✅ ${p.segments} 段`} · ${((p.elapsedMs ?? 0) / 1000).toFixed(1)}s`
            return (
              <details key={d.id} className="mb-2 rounded-md border border-gray-200">
                <summary className="flex cursor-pointer items-center gap-2 bg-gray-50 px-3 py-1.5 text-xs text-gray-600 hover:bg-gray-100">
                  <span className="font-medium">{label}</span>
                  <span className="ml-auto text-gray-400">{new Date(d.createdAt).toLocaleString()}</span>
                </summary>
                <pre className="custom-scrollbar max-h-48 overflow-y-auto whitespace-pre-wrap border-t border-gray-100 px-3 py-2 font-mono text-[11px] leading-relaxed text-gray-700">
                  {JSON.stringify(p, null, 2)}
                </pre>
              </details>
            )
          })}
        </Modal>
      )}

      {titleDebugOpen && (
        <Modal
          title="AI 命名调试:提示词与历史记录"
          onClose={() => setTitleDebugOpen(false)}
          wide
          footer={
            <>
              <button onClick={openTitleDebug} className="flex items-center gap-1.5 rounded-md border border-gray-200 px-4 py-2 text-sm text-gray-600 hover:bg-gray-50">
                <RefreshCw size={14} /> 刷新
              </button>
              <button onClick={() => setTitleDebugOpen(false)} className="rounded-md bg-gray-100 px-4 py-2 text-sm text-gray-700 hover:bg-gray-200">
                关闭
              </button>
            </>
          }
        >
          {!sDebugList.some((d) => {
            try {
              return JSON.parse(d.info).kind === 'autoname'
            } catch {
              return false
            }
          }) && <p className="py-8 text-center text-sm text-gray-400">还没有 AI 命名记录(生成总结时自动触发,或点魔法棒手动触发)</p>}
          {sDebugList.map((d) => {
            let p: any = null
            try {
              p = JSON.parse(d.info)
            } catch {}
            if (!p || p.kind !== 'autoname') return null
            const ok = p.status === 'completed'
            return (
              <div key={d.id} className="mb-2 rounded-md border border-gray-200">
                <div className="flex items-center gap-2 border-b border-gray-100 bg-gray-50 px-3 py-1.5 text-xs text-gray-600">
                  <span className="font-medium">
                    {ok ? `✅「${p.title}」` : `❌ 失败:${p.error ?? ''}`} ·{' '}
                    {p.source === 'context' ? '背景提取' : p.source === 'summary' ? '基于纪要' : '基于转录'} · {p.model} ·{' '}
                    {((p.elapsedMs ?? 0) / 1000).toFixed(1)}s ·{' '}
                    {p.force ? '手动触发' : '自动'}
                    {ok && p.applied === false ? '(未应用:标题已被手动修改)' : ''}
                  </span>
                  <span className="ml-auto text-gray-400">{new Date(d.createdAt).toLocaleString()}</span>
                </div>
                <div className="px-3 py-2 text-xs">
                  <details className="mb-1" open={false}>
                    <summary className="cursor-pointer text-gray-500 hover:text-gray-700">系统提示词</summary>
                    <pre className="custom-scrollbar mt-1 max-h-[24vh] overflow-y-auto whitespace-pre-wrap rounded bg-gray-50 p-2 font-mono text-[11px] leading-relaxed text-gray-700">
                      {p.prompt}
                    </pre>
                  </details>
                  <details>
                    <summary className="cursor-pointer text-gray-500 hover:text-gray-700">用户消息(会议背景 + 转录节选,{(p.input ?? '').length} 字)</summary>
                    <pre className="custom-scrollbar mt-1 max-h-[24vh] overflow-y-auto whitespace-pre-wrap rounded bg-gray-50 p-2 font-mono text-[11px] leading-relaxed text-gray-700">
                      {p.input}
                    </pre>
                  </details>
                </div>
              </div>
            )
          })}
        </Modal>
      )}

      {debugOpen && (
        <Modal
          title="纪要调试:每次生成的提示词、参数与耗时"
          onClose={() => setDebugOpen(false)}
          wide
          footer={
            <>
              <button onClick={openSummaryDebug} className="flex items-center gap-1.5 rounded-md border border-gray-200 px-4 py-2 text-sm text-gray-600 hover:bg-gray-50">
                <RefreshCw size={14} /> 刷新
              </button>
              <button onClick={() => setDebugOpen(false)} className="rounded-md bg-gray-100 px-4 py-2 text-sm text-gray-700 hover:bg-gray-200">
                关闭
              </button>
            </>
          }
        >
          {sDebugList.length === 0 && <p className="py-8 text-center text-sm text-gray-400">还没有生成记录(本次功能上线后的生成才会记录)</p>}
          {sDebugList.map((d) => {
            let p: any = null
            try {
              p = JSON.parse(d.info)
            } catch {}
            if (!p || p.kind === 'autoname') return null
            const ok = p.status === 'completed'
            const cancelled = p.error === '已取消'
            return (
              <div key={d.id} className="mb-2 rounded-md border border-gray-200">
                <div className="flex items-center gap-2 border-b border-gray-100 bg-gray-50 px-3 py-1.5 text-xs text-gray-600">
                  <span className="font-medium">
                    {p.templateName ?? p.template} · {p.model} · {ok ? '✅ 成功' : cancelled ? '⏹ 已取消' : '❌ 失败'} ·{' '}
                    {((p.elapsedMs ?? 0) / 1000).toFixed(1)}s
                  </span>
                  <span className="ml-auto text-gray-400">{new Date(d.createdAt).toLocaleString()}</span>
                </div>
                <div className="px-3 py-2 text-xs">
                  {p.error && !cancelled && <p className="mb-1.5 text-red-600">错误:{p.error}</p>}
                  <p className="mb-1.5 text-gray-400">
                    服务 {p.baseUrl} · 提示词 {p.promptChars} 字 · 转录输入 {p.inputChars} 字
                  </p>
                  <details className="mb-1">
                    <summary className="cursor-pointer text-gray-500 hover:text-gray-700">系统提示词(模板 + 背景 + 词表 + 修订)</summary>
                    <pre className="custom-scrollbar mt-1 max-h-[32vh] overflow-y-auto whitespace-pre-wrap rounded bg-gray-50 p-2 font-mono text-[11px] leading-relaxed text-gray-700">
                      {p.prompt}
                    </pre>
                  </details>
                  <details>
                    <summary className="cursor-pointer text-gray-500 hover:text-gray-700">用户消息(完整转录)</summary>
                    <pre className="custom-scrollbar mt-1 max-h-[24vh] overflow-y-auto whitespace-pre-wrap rounded bg-gray-50 p-2 font-mono text-[11px] leading-relaxed text-gray-700">
                      {p.input}
                    </pre>
                  </details>
                </div>
              </div>
            )
          })}
        </Modal>
      )}

      {labeling && (
        <SpeakerEnrollDialog
          meetingId={id!}
          segment={labeling}
          counts={{
            all: segments.filter((s) => s.speaker === labeling.speaker).length,
            window: segments.filter((s) => s.speaker === labeling.speaker && s.windowIdx === labeling.windowIdx).length,
          }}
          onClose={() => setLabeling(null)}
          onEnrolled={() => {
            load()
            api<Speaker[]>('/speakers').then(setSpeakers).catch(console.error)
          }}
        />
      )}
    </div>
  )
}
