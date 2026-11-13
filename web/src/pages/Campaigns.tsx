import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api, type Agent, type Campaign, type ContactList, type Rules } from '../api'
import { useSession } from '../App'
import { campaignStatus, days, say, when } from '../ru'

export const defaultRules: Rules = { from: '09:00', to: '20:00', days: [1, 2, 3, 4, 5, 6], max_attempts: 3, retry_minutes: 120, first_portion: 10, portion: 30 }

export function RulesEditor({ rules, onChange, disabled }: { rules: Rules; onChange: (r: Rules) => void; disabled?: boolean }) {
  const num = (key: keyof Rules) => (e: React.ChangeEvent<HTMLInputElement>) => onChange({ ...rules, [key]: Number(e.target.value) })
  return (
    <fieldset disabled={disabled} style={{ border: 0, padding: 0, margin: 0 }}>
      <div className="grid cols-3">
        <label>
          Звонить с
          <input type="time" min="08:00" max="21:00" value={rules.from} onChange={(e) => onChange({ ...rules, from: e.target.value })} />
        </label>
        <label>
          до (время Ташкента)
          <input type="time" min="08:00" max="21:00" value={rules.to} onChange={(e) => onChange({ ...rules, to: e.target.value })} />
        </label>
        <label>
          Попыток на человека
          <input type="number" min={1} max={5} value={rules.max_attempts} onChange={num('max_attempts')} />
        </label>
        <label>
          Пауза между попытками, минут
          <input type="number" min={10} max={1440} value={rules.retry_minutes} onChange={num('retry_minutes')} />
        </label>
        <label>
          Первая порция, человек
          <input type="number" min={1} max={50} value={rules.first_portion} onChange={num('first_portion')} />
        </label>
        <label>
          Следующие порции
          <input type="number" min={1} max={500} value={rules.portion} onChange={num('portion')} />
        </label>
      </div>
      <div className="row">
        {days.map((d, i) => (
          <label key={d} className="check">
            <input
              type="checkbox"
              checked={rules.days.includes(i + 1)}
              onChange={(e) => onChange({ ...rules, days: e.target.checked ? [...rules.days, i + 1].sort() : rules.days.filter((x) => x !== i + 1) })}
            />
            {d}
          </label>
        ))}
      </div>
    </fieldset>
  )
}

export default function Campaigns() {
  const { me } = useSession()
  const go = useNavigate()
  const [campaigns, setCampaigns] = useState<Campaign[] | null>(null)
  const [agents, setAgents] = useState<Agent[]>([])
  const [lists, setLists] = useState<ContactList[]>([])
  const [name, setName] = useState('')
  const [agent, setAgent] = useState('')
  const [list, setList] = useState('')
  const [line, setLine] = useState('')
  const [rules, setRules] = useState<Rules>(defaultRules)
  const [error, setError] = useState('')

  useEffect(() => {
    api('GET', '/campaigns').then((d) => setCampaigns(d.campaigns))
    api('GET', '/agents').then((d) => setAgents(d.agents))
    api('GET', '/lists').then((d) => setLists(d.lists))
  }, [])

  const chosen = agents.find((a) => a.id === agent)
  const chosenList = lists.find((l) => l.id === list)
  const lines = me.lines ?? []

  async function create(e: React.FormEvent) {
    e.preventDefault()
    setError('')
    try {
      const d = await api('POST', '/campaigns', { name: name || `${chosen?.name} · ${chosenList?.name}`, version_id: chosen?.latest?.id, list_id: list, line, rules })
      go(`/campaigns/${d.campaign.id}`)
    } catch (err) {
      setError(say(err))
    }
  }

  return (
    <>
      <div className="head">
        <div>
          <h1>Обзвоны</h1>
          <div className="muted">Обзвон — это агент, список и правила. Он идёт порциями и сам останавливается, если что-то пошло не так.</div>
        </div>
      </div>
      <form className="card" onSubmit={create}>
        <h2>Новый обзвон</h2>
        <div className="grid cols-3">
          <label>
            Агент
            <select value={agent} onChange={(e) => setAgent(e.target.value)} required>
              <option value="">Выберите</option>
              {agents.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name} (v{a.latest?.n}
                  {a.latest?.accepted_at ? '' : ', нужен тест'})
                </option>
              ))}
            </select>
          </label>
          <label>
            Список
            <select value={list} onChange={(e) => setList(e.target.value)} required>
              <option value="">Выберите</option>
              {lists.map((l) => (
                <option key={l.id} value={l.id}>
                  {l.name} ({l.stats.accepted})
                </option>
              ))}
            </select>
          </label>
          <label>
            Линия
            <select value={line} onChange={(e) => setLine(e.target.value)}>
              {lines.length !== 1 && <option value="">{lines.length ? 'Выберите' : 'Нет линий'}</option>}
              {lines.map((l) => (
                <option key={l.id} value={lines.length === 1 ? '' : l.id}>
                  {l.number}
                </option>
              ))}
            </select>
          </label>
        </div>
        <label>
          Название
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder={chosen && chosenList ? `${chosen.name} · ${chosenList.name}` : 'Название обзвона'} />
        </label>
        <RulesEditor rules={rules} onChange={setRules} />
        {chosen && !chosen.latest?.accepted_at && (
          <div className="note warn" style={{ marginTop: 12 }}>
            У версии {chosen.latest?.n} этого агента ещё нет принятого тестового звонка. Обзвон можно создать, но запустить получится только после теста:{' '}
            <Link to={`/agents/${chosen.id}`}>открыть агента</Link>.
          </div>
        )}
        {error && (
          <div className="note bad" style={{ marginTop: 12 }}>
            {error}
          </div>
        )}
        <div style={{ marginTop: 12 }}>
          <button className="primary" disabled={!agent || !list}>
            Создать черновик
          </button>
        </div>
      </form>
      <div className="card">
        {campaigns && campaigns.length === 0 && <div className="empty">Обзвонов пока нет.</div>}
        {campaigns && campaigns.length > 0 && (
          <table>
            <thead>
              <tr>
                <th>Обзвон</th>
                <th>Агент</th>
                <th>Состояние</th>
                <th>Люди</th>
                <th>Создан</th>
              </tr>
            </thead>
            <tbody>
              {campaigns.map((c) => {
                const total = Object.values(c.counts).reduce((a, b) => a + b, 0)
                const finished = (c.counts.done ?? 0) + (c.counts.failed ?? 0) + (c.counts.skipped ?? 0)
                return (
                  <tr key={c.id} className="link" onClick={() => go(`/campaigns/${c.id}`)}>
                    <td>
                      <Link to={`/campaigns/${c.id}`}>{c.name}</Link>
                    </td>
                    <td className="muted">
                      {c.agent_name} v{c.version_n}
                    </td>
                    <td>
                      <span className={`tag ${c.status === 'running' ? 'good' : c.status === 'paused' ? 'warn' : ''}`}>{campaignStatus[c.status]}</span>
                    </td>
                    <td className="num">
                      {finished} / {total}
                    </td>
                    <td className="muted">{when(c.created_at)}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </div>
    </>
  )
}
