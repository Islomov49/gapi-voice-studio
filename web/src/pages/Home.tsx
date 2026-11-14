import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, type Agent, type Campaign } from '../api'
import { useSession } from '../App'
import { campaignStatus, say } from '../ru'

// The first page: what stands between the project and its first campaign,
// in the order the Studio enforces it.
export default function Home() {
  const { me, reload } = useSession()
  const [agents, setAgents] = useState<Agent[]>([])
  const [campaigns, setCampaigns] = useState<Campaign[]>([])
  const [name, setName] = useState(me.workspace.name)
  const [tester, setTester] = useState(me.workspace.tester_phone)
  const [saved, setSaved] = useState('')
  const [error, setError] = useState('')
  const [ai, setAi] = useState<{ connected: boolean; model: string; default_model: string } | null>(null)
  const [aiKey, setAiKey] = useState('')
  const [aiModel, setAiModel] = useState('')
  const [aiNote, setAiNote] = useState('')
  const [aiError, setAiError] = useState('')

  useEffect(() => {
    api('GET', '/agents').then((d) => setAgents(d.agents))
    api('GET', '/campaigns').then((d) => setCampaigns(d.campaigns))
    api('GET', '/ai').then((d) => {
      setAi(d)
      setAiModel(d.model === d.default_model ? '' : d.model)
    })
  }, [])

  async function saveAi(e: React.FormEvent) {
    e.preventDefault()
    setAiError('')
    setAiNote('')
    try {
      const body: { key?: string; model: string } = { model: aiModel.trim() }
      if (aiKey.trim()) body.key = aiKey.trim()
      setAi(await api('PUT', '/ai', body))
      setAiKey('')
      setAiNote('Ключ проверен и сохранён.')
      await reload()
    } catch (err) {
      setAiError(say(err))
    }
  }
  async function dropAi() {
    setAiError('')
    setAiNote('')
    setAi(await api('DELETE', '/ai'))
    setAiModel('')
    await reload()
  }

  async function save(e: React.FormEvent) {
    e.preventDefault()
    setError('')
    try {
      await api('PUT', '/workspace', { name, tester_phone: tester })
      await reload()
      setSaved('Сохранено')
      setTimeout(() => setSaved(''), 2000)
    } catch (err) {
      setError(say(err))
    }
  }

  const line = (me.lines ?? []).length > 0
  const accepted = agents.some((a) => a.latest?.accepted_at)
  const steps = [
    { done: line, text: 'Телефонная линия подключена в кабинете Gapi', hint: 'Линия подключается в кабинете Gapi, раздел «Голосовые агенты».' },
    { done: !!me.workspace.tester_phone, text: 'Указан номер тестировщика', hint: 'Агент всегда сначала звонит ему.' },
    { done: agents.length > 0, text: 'Создан агент', to: '/agents' },
    { done: accepted, text: 'Тестовый звонок принят', hint: 'Без принятого тестового звонка обзвон не запускается.' },
    { done: campaigns.length > 0, text: 'Запущен обзвон', to: '/campaigns' },
  ]

  return (
    <>
      <div className="head">
        <div>
          <h1>Обзор</h1>
          <div className="muted">Путь от задачи до обзвона. Каждый шаг открывает следующий.</div>
        </div>
      </div>
      {me.gapi_error && <div className="note bad">Gapi не отвечает на запросы проекта: {me.gapi_error}. Звонки пока невозможны.</div>}
      {me.online === false && <div className="note warn">Голосовой сервис Gapi сейчас не в сети: звонки начнутся, когда он вернётся.</div>}
      <div className="grid cols-2">
        <div className="card">
          <h2>Что сделано</h2>
          <ol className="steps">
            {steps.map((s, i) => (
              <li key={i} className={s.done ? 'done' : ''}>
                <span className="dot">{s.done ? '✓' : i + 1}</span>
                <span>
                  {s.to && !s.done ? <Link to={s.to}>{s.text}</Link> : s.text}
                  {!s.done && s.hint && <span className="muted small"> · {s.hint}</span>}
                </span>
              </li>
            ))}
          </ol>
        </div>
        <form className="card" onSubmit={save}>
          <h2>Проект</h2>
          <label>
            Название
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Например, название компании" />
          </label>
          <label>
            Номер тестировщика
            <input value={tester} onChange={(e) => setTester(e.target.value)} placeholder="+998901234567" inputMode="tel" />
          </label>
          {error && <div className="note bad">{error}</div>}
          <div className="row">
            <button className="primary">Сохранить</button>
            <span className="muted small">{saved}</span>
          </div>
        </form>
      </div>
      <div className="card">
        <div className="grid cols-3">
          <div className="stat">
            <b>
              {me.open_calls} / {me.workspace.channels}
            </b>
            <span>каналов занято</span>
          </div>
          <div className="stat">
            <b>{(me.lines ?? []).length}</b>
            <span>линий в проекте Gapi</span>
          </div>
          <div className="stat">
            <b>{me.g_per_minute ?? '—'}</b>
            <span>единиц баланса Gapi за минуту звонка</span>
          </div>
        </div>
      </div>
      <form className="card" onSubmit={saveAi}>
        <div className="spread">
          <h2 style={{ margin: 0 }}>Разбор звонков через Gemini</h2>
          <span className={`tag ${ai?.connected ? 'good' : ''}`}>{ai?.connected ? 'ключ добавлен' : 'не обязательно'}</span>
        </div>
        <p className="muted small">
          С вашим ключом Gemini API Studio после каждого состоявшегося разговора сама заполняет форму по тексту звонка, пишет короткий итог и отмечает, что стоит
          перепроверить на слух. Ответ записывается только вместе со словами абонента, из которых он взят. Текст разговора при этом отправляется в Google; ключ
          хранится на этом компьютере в зашифрованном виде. Без ключа форму заполняете вы или ваш ассистент.
        </p>
        <div className="grid cols-2">
          <label>
            Ключ Gemini API
            <input
              type="password"
              autoComplete="off"
              value={aiKey}
              onChange={(e) => setAiKey(e.target.value)}
              placeholder={ai?.connected ? 'ключ сохранён; вставьте новый, чтобы заменить' : 'из aistudio.google.com, раздел API keys'}
            />
          </label>
          <label>
            Модель
            <input value={aiModel} onChange={(e) => setAiModel(e.target.value)} placeholder={ai?.default_model ?? ''} />
          </label>
        </div>
        {aiError && <div className="note bad">{aiError}</div>}
        <div className="row">
          <button className="primary" disabled={!ai || (!ai.connected && aiKey.trim().length < 20)}>
            {ai?.connected ? 'Сохранить' : 'Добавить ключ'}
          </button>
          {ai?.connected && (
            <button type="button" onClick={dropAi}>
              Удалить ключ
            </button>
          )}
          <span className="muted small">{aiNote}</span>
        </div>
      </form>
      {campaigns.length > 0 && (
        <div className="card">
          <h2>Обзвоны</h2>
          <table>
            <tbody>
              {campaigns.slice(0, 5).map((c) => (
                <tr key={c.id}>
                  <td>
                    <Link to={`/campaigns/${c.id}`}>{c.name}</Link>
                  </td>
                  <td className="muted">{c.agent_name}</td>
                  <td>
                    <span className={`tag ${c.status === 'running' ? 'good' : c.status === 'paused' ? 'warn' : ''}`}>{campaignStatus[c.status]}</span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  )
}
