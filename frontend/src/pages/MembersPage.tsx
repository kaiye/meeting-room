// Member management: one row per person (Chinese name / nicknames /
// english name / email / voiceprint), with multi-select merge for the
// same person recognized as several entries.
import { Check, Loader2, Merge, Pencil, Play, Plus, Search, Square, Trash2, X } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, type Speaker } from '../api'
import Modal from '../components/Modal'

type Draft = { id: string; name: string; aliases: string; nameEn: string; email: string }

export default function MembersPage() {
  const [members, setMembers] = useState<Speaker[]>([])
  const [search, setSearch] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [editing, setEditing] = useState<Draft | null>(null)
  const [mergeOpen, setMergeOpen] = useState(false)
  const [mergeTarget, setMergeTarget] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [playing, setPlaying] = useState<string | null>(null)
  const audioRef = useRef<HTMLAudioElement | null>(null)

  // Single shared player: one click plays, another stops.
  const playSample = (m: Speaker) => {
    audioRef.current?.pause()
    if (playing === m.id) {
      setPlaying(null)
      return
    }
    const a = new Audio(`/api/v1/speakers/${m.id}/sample/audio?v=${m.updatedAt}`)
    audioRef.current = a
    a.onended = () => setPlaying(null)
    a.play().then(() => setPlaying(m.id)).catch(() => setPlaying(null))
  }

  useEffect(() => () => audioRef.current?.pause(), [])

  const load = useCallback(async () => {
    try {
      setMembers(await api<Speaker[]>('/speakers'))
    } catch (e: any) {
      setErr(e.message)
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    if (!q) return members
    return members.filter((m) =>
      [m.name, m.nameEn, m.email, ...(m.aliases ?? [])].some((v) => (v ?? '').toLowerCase().includes(q)),
    )
  }, [members, search])

  const toggle = (id: string) =>
    setSelected((prev) => {
      const next = new Set(prev)
      next.has(id) ? next.delete(id) : next.add(id)
      return next
    })

  const save = async () => {
    if (!editing || !editing.name.trim()) return
    setBusy(true)
    try {
      const isNew = editing.id === ''
      await api(isNew ? '/speakers' : `/speakers/${editing.id}`, {
        method: isNew ? 'POST' : 'PUT',
        body: JSON.stringify({
          name: editing.name.trim(),
          aliases: editing.aliases.split(/[,，、\s]+/).filter(Boolean),
          nameEn: editing.nameEn.trim(),
          email: editing.email.trim(),
        }),
      })
      setEditing(null)
      load()
    } catch (e: any) {
      setErr(e.message)
    }
    setBusy(false)
  }

  const remove = async (m: Speaker) => {
    if (!confirm(`确认删除成员「${m.name}」及其声纹样本?`)) return
    try {
      await api(`/speakers/${m.id}`, { method: 'DELETE' })
      setSelected((prev) => {
        const next = new Set(prev)
        next.delete(m.id)
        return next
      })
      load()
    } catch (e: any) {
      setErr(e.message)
    }
  }

  const removeSelected = async () => {
    const victims = members.filter((m) => selected.has(m.id))
    const withVoice = victims.filter((m) => m.sampleSeconds > 0).length
    if (
      !confirm(
        `确认删除选中的 ${victims.length} 位成员?${withVoice > 0 ? `其中 ${withVoice} 位有声纹样本,一并删除。` : ''}`,
      )
    )
      return
    setBusy(true)
    try {
      for (const m of victims) await api(`/speakers/${m.id}`, { method: 'DELETE' })
      setSelected(new Set())
      load()
    } catch (e: any) {
      setErr(e.message)
    }
    setBusy(false)
  }

  const doMerge = async () => {
    if (!mergeTarget) return
    setBusy(true)
    try {
      await api('/speakers/merge', {
        method: 'POST',
        body: JSON.stringify({ targetId: mergeTarget, sourceIds: [...selected].filter((id) => id !== mergeTarget) }),
      })
      setMergeOpen(false)
      setSelected(new Set())
      load()
    } catch (e: any) {
      setErr(e.message)
    }
    setBusy(false)
  }

  const selectedMembers = members.filter((m) => selected.has(m.id))

  return (
    <div className="flex h-full flex-col bg-white">
      <div className="flex items-center gap-3 border-b border-gray-200 px-4 py-2.5">
        <h1 className="text-base font-semibold">成员管理</h1>
        <span className="text-xs text-gray-400">{members.length} 人</span>
        <div className="relative ml-2 w-64">
          <Search size={14} className="absolute left-2.5 top-2.5 text-gray-400" />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="搜索姓名 / 昵称 / 邮箱…"
            className="w-full rounded-md border border-gray-200 py-1.5 pl-8 pr-7 text-sm outline-none focus:border-blue-400"
          />
          {search && (
            <button onClick={() => setSearch('')} className="absolute right-2 top-2 text-gray-400 hover:text-gray-600">
              <X size={14} />
            </button>
          )}
        </div>
        <span className="ml-auto flex items-center gap-2">
          <button
            onClick={() => setEditing({ id: '', name: '', aliases: '', nameEn: '', email: '' })}
            className="flex items-center gap-1.5 rounded-md bg-blue-600 px-3 py-1.5 text-sm text-white hover:bg-blue-700"
          >
            <Plus size={14} /> 新建成员
          </button>
          {selected.size > 0 && <span className="text-xs text-gray-500">已选 {selected.size} 人</span>}
          <button
            onClick={() => {
              setMergeTarget([...selected][0] ?? '')
              setMergeOpen(true)
            }}
            disabled={selected.size < 2}
            title={selected.size < 2 ? '选中 2 个及以上成员后可合并' : '合并所选成员'}
            className="flex items-center gap-1.5 rounded-md border border-gray-200 px-3 py-1.5 text-sm text-gray-600 hover:bg-gray-50 disabled:opacity-40"
          >
            <Merge size={14} /> 合并
          </button>
          <button
            onClick={removeSelected}
            disabled={selected.size === 0 || busy}
            title="批量删除所选成员"
            className="flex items-center gap-1.5 rounded-md border border-gray-200 px-3 py-1.5 text-sm text-gray-600 hover:bg-red-50 hover:text-red-600 disabled:opacity-40"
          >
            <Trash2 size={14} /> 删除
          </button>
        </span>
      </div>

      {err && <div className="border-b border-red-200 bg-red-50 px-4 py-2 text-sm text-red-700">{err}</div>}

      <div className="custom-scrollbar min-h-0 flex-1 overflow-y-auto">
        <table className="w-full text-sm">
          <thead className="sticky top-0 z-10 bg-gray-50 text-left text-xs text-gray-500">
            <tr>
              <th className="w-10 px-3 py-2">
                <input
                  type="checkbox"
                  checked={filtered.length > 0 && filtered.every((m) => selected.has(m.id))}
                  onChange={(e) =>
                    setSelected(e.target.checked ? new Set(filtered.map((m) => m.id)) : new Set())
                  }
                  title="全选 / 全不选"
                />
              </th>
              <th className="px-3 py-2 font-medium">中文姓名</th>
              <th className="px-3 py-2 font-medium">常用昵称</th>
              <th className="px-3 py-2 font-medium">英文名</th>
              <th className="px-3 py-2 font-medium">邮箱</th>
              <th className="w-28 px-3 py-2 font-medium">声纹</th>
              <th className="w-20 px-3 py-2"></th>
            </tr>
          </thead>
          <tbody>
            {filtered.map((m) => (
              <tr key={m.id} className={`group border-b border-gray-100 ${selected.has(m.id) ? 'bg-blue-50/50' : 'hover:bg-gray-50'}`}>
                <td className="px-3 py-2">
                  <input type="checkbox" checked={selected.has(m.id)} onChange={() => toggle(m.id)} />
                </td>
                <td className="px-3 py-2 font-medium">{m.name}</td>
                <td className="px-3 py-2 text-gray-500">{(m.aliases ?? []).join(' / ') || '—'}</td>
                <td className="px-3 py-2 text-gray-500">{m.nameEn || '—'}</td>
                <td className="px-3 py-2 text-gray-500">{m.email || '—'}</td>
                <td className="px-3 py-2">
                  {m.sampleSeconds > 0 ? (
                    <span className="flex items-center gap-1.5">
                      <button
                        onClick={() => playSample(m)}
                        title={playing === m.id ? '停止' : '试听声纹'}
                        className="flex h-6 w-6 items-center justify-center rounded-full bg-blue-600 text-white hover:bg-blue-700"
                      >
                        {playing === m.id ? <Square size={9} /> : <Play size={10} className="ml-0.5" />}
                      </button>
                      <span className="text-xs text-gray-400">{m.sampleSeconds.toFixed(1)}s</span>
                    </span>
                  ) : (
                    <span className="text-xs text-gray-300">无声纹</span>
                  )}
                </td>
                <td className="px-3 py-2">
                  <span className="flex gap-1 opacity-0 group-hover:opacity-100">
                    <button
                      onClick={() =>
                        setEditing({
                          id: m.id,
                          name: m.name,
                          aliases: (m.aliases ?? []).join(', '),
                          nameEn: m.nameEn ?? '',
                          email: m.email ?? '',
                        })
                      }
                      title="编辑"
                      className="rounded p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600"
                    >
                      <Pencil size={14} />
                    </button>
                    <button onClick={() => remove(m)} title="删除成员" className="rounded p-1.5 text-gray-400 hover:bg-red-50 hover:text-red-600">
                      <Trash2 size={14} />
                    </button>
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {filtered.length === 0 && (
          <p className="py-16 text-center text-sm text-gray-400">
            {search ? '没有匹配的成员' : '还没有成员。在会议转录里点说话人标签标注,或从日历日程带入参会人。'}
          </p>
        )}
      </div>

      {editing && (
        <Modal
          title={editing.id === '' ? '新建成员' : '编辑成员'}
          onClose={() => setEditing(null)}
          wide
          footer={
            <>
              <button onClick={() => setEditing(null)} className="rounded-md px-4 py-2 text-sm text-gray-600 hover:bg-gray-100">
                取消
              </button>
              <button onClick={save} disabled={busy} className="flex items-center gap-1.5 rounded-md bg-blue-600 px-4 py-2 text-sm text-white hover:bg-blue-700">
                {busy && <Loader2 size={14} className="animate-spin" />} 保存
              </button>
            </>
          }
        >
          <div className="grid grid-cols-2 gap-3">
            {(
              [
                ['name', '中文姓名(显示名)', '如: 张三'],
                ['aliases', '常用昵称(逗号分隔)', '如: 老王, wangwei'],
                ['nameEn', '英文名', '如: David'],
                ['email', '邮箱', 'name@company.com'],
              ] as const
            ).map(([key, label, ph]) => (
              <div key={key}>
                <label className="mb-1 block text-xs font-medium text-gray-500">{label}</label>
                <input
                  value={editing[key]}
                  placeholder={ph}
                  onChange={(e) => setEditing({ ...editing, [key]: e.target.value })}
                  className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400"
                />
              </div>
            ))}
          </div>
          <p className="mt-3 text-xs text-gray-400">
            姓名、昵称、英文名、邮箱都会用于匹配:标注说话人时输入其中任意一个都归到此人;日历参会人也按这些字段自动对应。
          </p>
        </Modal>
      )}

      {mergeOpen && (
        <Modal
          title={`合并 ${selected.size} 位成员`}
          onClose={() => setMergeOpen(false)}
          wide
          footer={
            <>
              <button onClick={() => setMergeOpen(false)} className="rounded-md px-4 py-2 text-sm text-gray-600 hover:bg-gray-100">
                取消
              </button>
              <button onClick={doMerge} disabled={busy || !mergeTarget} className="flex items-center gap-1.5 rounded-md bg-blue-600 px-4 py-2 text-sm text-white hover:bg-blue-700 disabled:opacity-50">
                {busy && <Loader2 size={14} className="animate-spin" />} 确认合并
              </button>
            </>
          }
        >
          <p className="mb-2 text-sm text-gray-600">选择保留的主记录(其显示名将作为合并后的姓名):</p>
          {selectedMembers.map((m) => (
            <label
              key={m.id}
              className={`mb-1.5 flex cursor-pointer items-center gap-2 rounded-md border px-3 py-2 ${mergeTarget === m.id ? 'border-blue-400 bg-blue-50/60' : 'border-gray-200'}`}
            >
              <input type="radio" checked={mergeTarget === m.id} onChange={() => setMergeTarget(m.id)} />
              <span className="text-sm font-medium">{m.name}</span>
              <span className="text-xs text-gray-400">
                {[(m.aliases ?? []).join('/'), m.nameEn, m.email].filter(Boolean).join(' · ')}
              </span>
              <span className="ml-auto text-xs text-gray-400">
                {m.sampleSeconds > 0 ? `声纹 ${m.sampleSeconds.toFixed(1)}s · ${new Date(m.updatedAt).toLocaleDateString()}` : '无声纹'}
              </span>
            </label>
          ))}
          <ul className="mt-3 list-disc pl-5 text-xs leading-relaxed text-gray-500">
            <li>其余成员的姓名、昵称、英文名、邮箱全部并入主记录的常用昵称</li>
            <li>声纹保留所有记录中<b>最新</b>的一段,其余样本删除</li>
            <li>历史转录中这些人的说话人标签统一改为主记录的姓名</li>
          </ul>
          <p className="mt-2 flex items-center gap-1 text-xs text-gray-400">
            <Check size={12} /> 合并不可撤销,但可以随时重新标注声纹。
          </p>
        </Modal>
      )}
    </div>
  )
}
