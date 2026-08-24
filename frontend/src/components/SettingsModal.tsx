// Settings — 转录服务 / 总结 LLM / 总结模板 three tabs.
// Model names are picked from the service's live /v1/models list (proxied
// by the server), not typed by hand; templates are DB-backed and editable.
import { Loader2, Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { api, type Settings, type Template } from '../api'
import Modal from './Modal'

type Section = {
  baseUrl: string
  model: string
  apiKey: string
  liveBaseUrl?: string
  liveModel?: string
  liveApiKey?: string
  language?: string
  chunkSeconds?: number
  liveChunkSeconds?: number
}
const empty: Section = { baseUrl: '', model: '', apiKey: '' }
type FullTemplate = Template & { prompt: string; builtin: boolean }

export default function SettingsModal({ onClose }: { onClose: () => void }) {
  const [tab, setTab] = useState<'transcribe' | 'summary' | 'context' | 'templates' | 'calendar'>('transcribe')
  const [calendarUrl, setCalendarUrl] = useState('')
  const [transcribe, setTranscribe] = useState<Section>(empty)
  const [summary, setSummary] = useState<Section>(empty)
  const [context, setContext] = useState({ background: '', glossary: '' })
  // Structured glossary rows: hotword (correct term, sent to the ASR as a
  // decoding-bias hotword) + its common mis-transcriptions.
  const [glossRows, setGlossRows] = useState<Array<{ term: string; wrongs: string }>>([])
  const [manualModel, setManualModel] = useState<Record<string, boolean>>({})

  const parseGloss = (text: string) =>
    text
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean)
      .map((l) => {
        const i = l.lastIndexOf('(常被误转录为:')
        if (i >= 0 && l.endsWith(')')) return { term: l.slice(0, i).trim(), wrongs: l.slice(i + 8, -1).trim() }
        return { term: l, wrongs: '' }
      })

  const serializeGloss = () =>
    glossRows
      .filter((r) => r.term.trim())
      .map((r) => {
        const ws = r.wrongs.split(/[,,、]+/).map((w) => w.trim()).filter(Boolean)
        return ws.length > 0 ? `${r.term.trim()}(常被误转录为:${ws.join('、')})` : r.term.trim()
      })
      .join('\n')
  const [models, setModels] = useState<Record<string, string[]>>({})
  const [modelErr, setModelErr] = useState<Record<string, string>>({})
  const [fetching, setFetching] = useState(false)
  const [hint, setHint] = useState('')
  const [busy, setBusy] = useState(false)

  // Templates tab state
  const [templates, setTemplates] = useState<FullTemplate[]>([])
  const [editing, setEditing] = useState<FullTemplate | null>(null)
  const [isNew, setIsNew] = useState(false)

  // Speakers tab state

  const loadTemplates = useCallback(() => {
    api<FullTemplate[]>('/summary-templates').then(setTemplates).catch(console.error)
  }, [])

  useEffect(() => {
    api<Settings>('/settings')
      .then((s) => {
        setTranscribe(s.transcribe)
        setSummary(s.summary)
        setContext(s.context ?? { background: '', glossary: '' })
        setGlossRows(parseGloss(s.context?.glossary ?? ''))
        setCalendarUrl(s.calendar?.icsUrl ?? '')
        if (s.transcribe.baseUrl) fetchModels('transcribe', s.transcribe)
        if (s.summary.baseUrl) fetchModels('summary', s.summary)
      })
      .catch((e) => setHint('读取配置失败: ' + e.message))
    loadTemplates()
  }, [loadTemplates])

  const fetchModels = async (kind: string, section: Section) => {
    if (!section.baseUrl.trim()) return
    setFetching(true)
    setModelErr((m) => ({ ...m, [kind]: '' }))
    try {
      const r = await api<{ ok: boolean; models?: string[]; error?: string }>('/settings/models', {
        method: 'POST',
        body: JSON.stringify({ baseUrl: section.baseUrl.trim(), apiKey: section.apiKey }),
      })
      if (r.ok && r.models) setModels((m) => ({ ...m, [kind]: r.models! }))
      else setModelErr((m) => ({ ...m, [kind]: r.error || '获取失败' }))
    } catch (e: any) {
      setModelErr((m) => ({ ...m, [kind]: e.message }))
    }
    setFetching(false)
  }

  // Models of the separate live-recognition service (when its own base URL
  // is configured); falls back to the batch service's list otherwise.
  const fetchLiveModels = async () => {
    const url = (transcribe.liveBaseUrl ?? '').trim()
    if (!url) return
    setFetching(true)
    setModelErr((m) => ({ ...m, live: '' }))
    try {
      const r = await api<{ ok: boolean; models?: string[]; error?: string }>('/settings/models', {
        method: 'POST',
        body: JSON.stringify({ baseUrl: url, apiKey: transcribe.liveApiKey ?? '' }),
      })
      if (r.ok && r.models) setModels((m) => ({ ...m, live: r.models! }))
      else setModelErr((m) => ({ ...m, live: r.error || '获取失败' }))
    } catch (e: any) {
      setModelErr((m) => ({ ...m, live: e.message }))
    }
    setFetching(false)
  }

  const test = async () => {
    setBusy(true)
    setHint('测试中…')
    try {
      const r = await api<{ ok: boolean; baseUrl?: string; error?: string }>('/settings/transcribe/test', {
        method: 'POST',
        body: JSON.stringify(transcribe),
      })
      setHint(r.ok ? `✅ 连接成功: ${r.baseUrl}` : `❌ ${r.error}`)
    } catch (e: any) {
      setHint('❌ ' + e.message)
    }
    setBusy(false)
  }

  const save = async () => {
    setBusy(true)
    try {
      if (tab === 'transcribe') await api('/settings/transcribe', { method: 'PUT', body: JSON.stringify(transcribe) })
      else if (tab === 'summary') await api('/settings/summary', { method: 'PUT', body: JSON.stringify(summary) })
      else if (tab === 'context') {
        const glossary = serializeGloss()
        setContext({ ...context, glossary })
        await api('/settings/context', { method: 'PUT', body: JSON.stringify({ ...context, glossary }) })
      } else if (tab === 'calendar') {
        await api('/settings/calendar', { method: 'PUT', body: JSON.stringify({ icsUrl: calendarUrl.trim() }) })
      }
      setHint('已保存')
    } catch (e: any) {
      setHint('❌ ' + e.message)
    }
    setBusy(false)
  }

  const saveTemplate = async () => {
    if (!editing) return
    setBusy(true)
    try {
      if (isNew) await api('/summary-templates', { method: 'POST', body: JSON.stringify(editing) })
      else await api(`/summary-templates/${editing.id}`, { method: 'PUT', body: JSON.stringify(editing) })
      setEditing(null)
      loadTemplates()
      setHint('模板已保存')
    } catch (e: any) {
      setHint('❌ ' + e.message)
    }
    setBusy(false)
  }

  const deleteTemplate = async (t: FullTemplate) => {
    if (!confirm(`确认删除模板「${t.name}」?`)) return
    try {
      await api(`/summary-templates/${t.id}`, { method: 'DELETE' })
      loadTemplates()
    } catch (e: any) {
      setHint('❌ ' + e.message)
    }
  }

  const sectionForm = (kind: 'transcribe' | 'summary') => {
    const section = kind === 'transcribe' ? transcribe : summary
    const setSection = kind === 'transcribe' ? setTranscribe : setSummary
    const list = models[kind] ?? []
    const err = modelErr[kind]
    const options = section.model && !list.includes(section.model) ? [section.model, ...list] : list
    return (
      <>
        <div className="mb-3">
          <label className="mb-1 block text-xs font-medium text-gray-500">
            服务地址{kind === 'transcribe' ? '(本地 / 局域网 / https://api.openai.com/v1)' : '(如 Ollama: http://127.0.0.1:11434/v1)'}
          </label>
          <input
            value={section.baseUrl}
            placeholder={kind === 'transcribe' ? 'http://127.0.0.1:7860/v1' : 'http://127.0.0.1:11434/v1'}
            onChange={(e) => setSection({ ...section, baseUrl: e.target.value })}
            onBlur={() => fetchModels(kind, section)}
            className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400"
          />
        </div>
        <div className="mb-3">
          <label className="mb-1 flex items-center gap-2 text-xs font-medium text-gray-500">
            模型
            <button onClick={() => fetchModels(kind, section)} title="刷新模型列表" className="rounded p-0.5 text-gray-400 hover:text-blue-600">
              <RefreshCw size={12} className={fetching ? 'animate-spin' : ''} />
            </button>
            <button
              onClick={() => setManualModel((m) => ({ ...m, [kind]: !m[kind] }))}
              title={manualModel[kind] ? '从列表选择' : '手动输入模型名(调试用)'}
              className={`rounded p-0.5 hover:text-blue-600 ${manualModel[kind] ? 'text-blue-500' : 'text-gray-400'}`}
            >
              <Pencil size={12} />
            </button>
            {err && <span className="font-normal text-red-500">获取失败:{err}(可手动输入)</span>}
          </label>
          {options.length > 0 && !err && !manualModel[kind] ? (
            <select
              value={section.model}
              onChange={(e) => setSection({ ...section, model: e.target.value })}
              className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400"
            >
              {!section.model && <option value="">— 选择模型 —</option>}
              {options.map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </select>
          ) : (
            <input
              value={section.model}
              placeholder={err ? '手动输入模型名' : '先填服务地址,自动获取模型列表'}
              onChange={(e) => setSection({ ...section, model: e.target.value })}
              className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400"
            />
          )}
        </div>
        {kind === 'transcribe' && (
          <div className="mb-3 rounded-md border border-dashed border-gray-200 p-3">
            <p className="mb-2 text-xs font-medium text-gray-500">
              实时识别服务(仅实时字幕使用;各项留空 = 与上面的批量转录服务相同)
            </p>
            <div className="mb-2">
              <label className="mb-1 block text-[11px] text-gray-400">服务地址</label>
              <input
                value={section.liveBaseUrl ?? ''}
                placeholder="留空 = 同批量服务"
                onChange={(e) => setSection({ ...section, liveBaseUrl: e.target.value })}
                onBlur={fetchLiveModels}
                className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400"
              />
            </div>
            <div className="mb-2 grid grid-cols-2 gap-2">
              <div>
                <label className="mb-1 flex items-center gap-1.5 text-[11px] text-gray-400">
                  模型
                  <button onClick={fetchLiveModels} title="刷新实时服务的模型列表" className="rounded p-0.5 text-gray-400 hover:text-blue-600">
                    <RefreshCw size={11} className={fetching ? 'animate-spin' : ''} />
                  </button>
                  <button
                    onClick={() => setManualModel((m) => ({ ...m, live: !m.live }))}
                    title={manualModel.live ? '从列表选择' : '手动输入模型名(调试用)'}
                    className={`rounded p-0.5 hover:text-blue-600 ${manualModel.live ? 'text-blue-500' : 'text-gray-400'}`}
                  >
                    <Pencil size={11} />
                  </button>
                  {modelErr['live'] && <span className="text-red-500">获取失败:{modelErr['live']}</span>}
                </label>
                {manualModel.live ? (
                  <input
                    value={section.liveModel ?? ''}
                    placeholder="手动输入模型名;留空 = 同批量模型"
                    onChange={(e) => setSection({ ...section, liveModel: e.target.value })}
                    className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400"
                  />
                ) : (
                  <select
                    value={section.liveModel ?? ''}
                    onChange={(e) => setSection({ ...section, liveModel: e.target.value })}
                    className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400"
                  >
                    <option value="">同批量模型</option>
                    {((section.liveBaseUrl ?? '').trim() ? (models['live'] ?? []) : (models[kind] ?? [])).map((m) => (
                      <option key={m} value={m}>
                        {m}
                      </option>
                    ))}
                    {section.liveModel &&
                      !((section.liveBaseUrl ?? '').trim() ? (models['live'] ?? []) : (models[kind] ?? [])).includes(section.liveModel) && (
                        <option value={section.liveModel}>{section.liveModel}</option>
                      )}
                  </select>
                )}
              </div>
              <div>
                <label className="mb-1 block text-[11px] text-gray-400">API Key</label>
                <input
                  type="password"
                  value={section.liveApiKey ?? ''}
                  placeholder="留空 = 同批量服务"
                  onChange={(e) => setSection({ ...section, liveApiKey: e.target.value })}
                  onBlur={fetchLiveModels}
                  className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400"
                />
              </div>
            </div>
            <p className="text-[11px] text-gray-400">导入/重新转录/停止后的尾段补转仍使用上面的批量服务;适合实时接快服务、事后重转用准服务对比测试</p>
          </div>
        )}
        <div className="mb-3">
          <label className="mb-1 block text-xs font-medium text-gray-500">API Key(本地服务留空)</label>
          <input
            type="password"
            value={section.apiKey}
            onChange={(e) => setSection({ ...section, apiKey: e.target.value })}
            onBlur={() => fetchModels(kind, section)}
            className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400"
          />
        </div>
        {kind === 'transcribe' && (
          <div className="mb-3 grid grid-cols-3 gap-3">
            <div>
              <label className="mb-1 block text-xs font-medium text-gray-500">转录语言</label>
              <select
                value={section.language || 'zh'}
                onChange={(e) => setSection({ ...section, language: e.target.value })}
                className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400"
              >
                <option value="zh">中文</option>
                <option value="en">English</option>
                <option value="auto">自动检测</option>
              </select>
              <p className="mt-1 text-[11px] text-gray-400">录音/导入的默认语言</p>
            </div>
            <div>
              <label className="mb-1 block text-xs font-medium text-gray-500">分块秒数(导入/重转)</label>
              <input
                type="number"
                min={15}
                value={section.chunkSeconds || ''}
                onChange={(e) => setSection({ ...section, chunkSeconds: parseInt(e.target.value) || 0 })}
                className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400"
              />
              <p className="mt-1 text-[11px] text-gray-400">MOSS 建议 600;funasr 建议 60</p>
            </div>
            <div>
              <label className="mb-1 block text-xs font-medium text-gray-500">实时分块秒数(字幕延迟)</label>
              <input
                type="number"
                min={15}
                value={section.liveChunkSeconds || ''}
                onChange={(e) => setSection({ ...section, liveChunkSeconds: parseInt(e.target.value) || 0 })}
                className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400"
              />
              <p className="mt-1 text-[11px] text-gray-400">MOSS 建议 300;funasr 建议 45,越小字幕越快</p>
            </div>
          </div>
        )}
        <p className="text-xs text-gray-400">
          {kind === 'transcribe'
            ? '兼容 OpenAI /v1/audio/transcriptions 协议;模型名含 "diarize" 时自动启用说话人分离。'
            : '兼容 OpenAI /v1/chat/completions 协议,用于生成会议总结。'}
        </p>
      </>
    )
  }

  return (
    <Modal
      title="设置"
      onClose={onClose}
      wide
      footer={
        tab === 'templates' ? (
          editing ? (
            <>
              <button onClick={() => setEditing(null)} className="rounded-md px-4 py-2 text-sm text-gray-600 hover:bg-gray-100">
                取消
              </button>
              <button onClick={saveTemplate} disabled={busy} className="rounded-md bg-blue-600 px-4 py-2 text-sm text-white hover:bg-blue-700">
                保存模板
              </button>
            </>
          ) : undefined
        ) : (
          <>
            {tab === 'transcribe' && (
              <button onClick={test} disabled={busy} className="rounded-md border border-gray-200 px-4 py-2 text-sm text-gray-600 hover:bg-gray-50">
                测试连接
              </button>
            )}
            <button onClick={save} disabled={busy} className="flex items-center gap-1.5 rounded-md bg-blue-600 px-4 py-2 text-sm text-white hover:bg-blue-700">
              {busy && <Loader2 size={14} className="animate-spin" />} 保存
            </button>
          </>
        )
      }
    >
      <div className="mb-4 flex gap-1 border-b border-gray-200">
        {(
          [
            ['transcribe', '转录服务'],
            ['summary', '总结 LLM'],
            ['context', '名词表'],
            ['templates', '总结模板'],
            ['calendar', '日历'],
          ] as const
        ).map(([k, label]) => (
          <button
            key={k}
            onClick={() => {
              setTab(k)
              setHint('')
              setEditing(null)
            }}
            className={`border-b-2 px-4 py-2 text-sm font-medium ${tab === k ? 'border-blue-600 text-blue-600' : 'border-transparent text-gray-500 hover:text-gray-700'}`}
          >
            {label}
          </button>
        ))}
      </div>

      <div className="custom-scrollbar max-h-[60vh] overflow-y-auto pr-1">
        {tab === 'transcribe' && sectionForm('transcribe')}
        {tab === 'summary' && sectionForm('summary')}

        {tab === 'context' && (
          <>
            <div className="mb-2 flex items-center gap-3 px-1 text-xs font-medium text-gray-500">
              <span className="w-[38%]">热词(纠正后的名词,随转录请求下发做解码偏置)</span>
              <span className="flex-1">常见错误(误转录形式,顿号/逗号分隔;生成纪要时纠正)</span>
              <span className="w-6" />
            </div>
            {glossRows.map((r, i) => (
              <div key={i} className="mb-1.5 flex items-center gap-3">
                <input
                  value={r.term}
                  placeholder="如: referral"
                  onChange={(e) => setGlossRows((rows) => rows.map((x, j) => (j === i ? { ...x, term: e.target.value } : x)))}
                  className="w-[38%] rounded-md border border-gray-300 px-3 py-1.5 text-sm outline-none focus:border-blue-400"
                />
                <input
                  value={r.wrongs}
                  placeholder="如: refeeral、referal"
                  onChange={(e) => setGlossRows((rows) => rows.map((x, j) => (j === i ? { ...x, wrongs: e.target.value } : x)))}
                  className="flex-1 rounded-md border border-gray-300 px-3 py-1.5 text-sm outline-none focus:border-blue-400"
                />
                <button
                  onClick={() => setGlossRows((rows) => rows.filter((_, j) => j !== i))}
                  title="删除词条"
                  className="w-6 shrink-0 rounded p-1 text-gray-300 hover:bg-red-50 hover:text-red-500"
                >
                  <Trash2 size={14} />
                </button>
              </div>
            ))}
            <button
              onClick={() => setGlossRows((rows) => [...rows, { term: '', wrongs: '' }])}
              className="mt-1 flex w-full items-center justify-center gap-1.5 rounded-md border border-dashed border-gray-300 py-2 text-sm text-gray-500 hover:border-blue-400 hover:text-blue-600"
            >
              <Plus size={14} /> 添加词条
            </button>
            <p className="mt-3 text-xs text-gray-400">
              热词列会作为 hotwords 随转录请求发给本地引擎(解码期术语偏置;指向 OpenAI 官方时不发送);热词 +
              常见错误会一并注入纪要生成,用于纠正转录相近词。会议页的「改写」可一键沉淀到这里。
            </p>
          </>
        )}

        {tab === 'calendar' && (
          <>
            <div className="mb-3">
              <label className="mb-1 block text-xs font-medium text-gray-500">日历订阅地址(ICS)</label>
              <input
                value={calendarUrl}
                placeholder="https://calendar.google.com/calendar/ical/…/basic.ics"
                onChange={(e) => setCalendarUrl(e.target.value)}
                className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400"
              />
            </div>
            <div className="rounded-md bg-gray-50 px-3 py-2 text-xs leading-relaxed text-gray-500">
              <p className="mb-1 font-medium text-gray-600">获取 Google Calendar 的私密地址:</p>
              <p>
                Google 日历网页版 → 设置 → 左侧选择你的日历 → 「集成日历」→ 复制「<b>iCal 格式的私密地址</b>
                」(Secret address in iCal format)粘贴到上面。
              </p>
              <p className="mt-1.5">
                配置后主页左侧会显示今天的日程,点击日程即可带着主题、时间、参会人直接开始录音。该地址包含私密令牌,请勿外传;Outlook/Apple
                日历的 ICS 订阅地址同样适用。注意:Google 对私密 ICS 有缓存,新建的日程可能延迟几分钟到几小时才出现。
              </p>
            </div>
          </>
        )}

        {tab === 'templates' && !editing && (
          <>
            {templates.map((t) => (
              <div key={t.id} className="group mb-2 flex items-start gap-3 rounded-md border border-gray-200 px-3 py-2.5">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2 text-sm font-medium">
                    {t.name}
                    {t.builtin && <span className="rounded bg-gray-100 px-1.5 py-0.5 text-[10px] text-gray-500">预设</span>}
                  </div>
                  <div className="truncate text-xs text-gray-400">{t.description}</div>
                </div>
                <span className="flex shrink-0 gap-1 opacity-0 group-hover:opacity-100">
                  <button
                    onClick={() => {
                      setEditing({ ...t })
                      setIsNew(false)
                    }}
                    className="rounded p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-600"
                  >
                    <Pencil size={14} />
                  </button>
                  <button onClick={() => deleteTemplate(t)} className="rounded p-1.5 text-gray-400 hover:bg-red-50 hover:text-red-600">
                    <Trash2 size={14} />
                  </button>
                </span>
              </div>
            ))}
            <button
              onClick={() => {
                setEditing({ id: '', name: '', description: '', prompt: '', builtin: false })
                setIsNew(true)
              }}
              className="flex w-full items-center justify-center gap-1.5 rounded-md border border-dashed border-gray-300 py-2.5 text-sm text-gray-500 hover:border-blue-400 hover:text-blue-600"
            >
              <Plus size={14} /> 新建模板
            </button>
          </>
        )}

        {tab === 'templates' && editing && (
          <div className="space-y-3">
            <div>
              <label className="mb-1 block text-xs font-medium text-gray-500">模板名称</label>
              <input
                value={editing.name}
                onChange={(e) => setEditing({ ...editing, name: e.target.value })}
                className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400"
              />
            </div>
            <div>
              <label className="mb-1 block text-xs font-medium text-gray-500">描述(显示在模板下拉里)</label>
              <input
                value={editing.description}
                onChange={(e) => setEditing({ ...editing, description: e.target.value })}
                className="w-full rounded-md border border-gray-300 px-3 py-2 text-sm outline-none focus:border-blue-400"
              />
            </div>
            <div>
              <label className="mb-1 block text-xs font-medium text-gray-500">提示词(发给 LLM 的完整指令,转录内容会作为用户消息附在后面)</label>
              <textarea
                value={editing.prompt}
                onChange={(e) => setEditing({ ...editing, prompt: e.target.value })}
                rows={12}
                className="w-full resize-y rounded-md border border-gray-300 px-3 py-2 font-mono text-xs leading-relaxed outline-none focus:border-blue-400"
              />
            </div>
          </div>
        )}
      </div>

      {hint && <p className="mt-3 text-sm text-gray-600">{hint}</p>}
    </Modal>
  )
}
