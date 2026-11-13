import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, type Agent, type Spec } from '../api'
import { say, when } from '../ru'

type Template = { id: string; title: string; description: string; spec: Spec }

export default function Agents() {
  const [agents, setAgents] = useState<Agent[] | null>(null)
  const [templates, setTemplates] = useState<Template[]>([])
  const [error, setError] = useState('')
  const go = useNavigate()

  useEffect(() => {
    api('GET', '/agents').then((d) => setAgents(d.agents))
    api('GET', '/templates').then((d) => setTemplates(d.templates))
  }, [])

  async function create(body: object) {
    setError('')
    try {
      const d = await api('POST', '/agents', body)
      go(`/agents/${d.agent.id}`)
    } catch (e) {
      setError(say(e))
    }
  }

  return (
    <>
      <div className="head">
        <div>
          <h1>Агенты</h1>
          <div className="muted">Агент — это сценарий разговора и форма с тем, что нужно выяснить.</div>
        </div>
      </div>
      {error && <div className="note bad">{error}</div>}
      <h2>Начать с задачи</h2>
      <div className="grid cols-2">
        {templates.map((t) => (
          <div className="card" key={t.id} style={{ marginBottom: 0 }}>
            <h3>{t.title}</h3>
            <p className="muted small">{t.description}</p>
            <button onClick={() => create({ template: t.id })}>Создать агента</button>
          </div>
        ))}
        <div className="card" style={{ marginBottom: 0 }}>
          <h3>Свой сценарий</h3>
          <p className="muted small">Пустой агент: сценарий и форму вы пишете сами.</p>
          <button onClick={() => create({ name: 'Новый агент', spec: { company: '', script: '', voice: 'charos', max_call_seconds: 300 } })}>
            Создать пустого
          </button>
        </div>
      </div>
      <h2 style={{ marginTop: 28 }}>Ваши агенты</h2>
      <div className="card">
        {agents && agents.length === 0 && <div className="empty">Агентов пока нет. Выберите задачу выше.</div>}
        {agents && agents.length > 0 && (
          <table>
            <thead>
              <tr>
                <th>Агент</th>
                <th>Компания</th>
                <th>Версия</th>
                <th>Тестовый звонок</th>
                <th>Изменён</th>
              </tr>
            </thead>
            <tbody>
              {agents.map((a) => (
                <tr key={a.id} className="link" onClick={() => go(`/agents/${a.id}`)}>
                  <td>
                    <Link to={`/agents/${a.id}`}>{a.name}</Link>
                  </td>
                  <td>{a.latest?.spec.company}</td>
                  <td className="num">{a.latest?.n}</td>
                  <td>{a.latest?.accepted_at ? <span className="tag good">Принят</span> : <span className="tag warn">Нужен</span>}</td>
                  <td className="muted">{when(a.latest?.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  )
}
