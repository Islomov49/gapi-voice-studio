import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, ApiError, type Campaign, type Field, type Rules, type Target } from '../api'
import { usePoll } from '../App'
import { campaignStatus, pauseReason, result, say, seconds, targetStatus, when } from '../ru'
import { RulesEditor } from './Campaigns'
import CrmRulesCard from './CrmRulesCard'

type FieldStats = { key: string; title: string; type: string; answered: number; values?: { value: string; n: number }[]; average?: number }
type Detail = { campaign: Campaign; results: Record<string, number>; form: FieldStats[]; calls: { placed: number; answered: number; seconds: number; charged_g: number } }

export default function CampaignPage() {
  const { id } = useParams()
  const go = useNavigate()
  const [detail, setDetail] = useState<Detail | null>(null)
  const [targets, setTargets] = useState<Target[]>([])
  const [form, setForm] = useState<Field[]>([])
  const [filter, setFilter] = useState('')
  const [numbers, setNumbers] = useState(false)
  const [rules, setRules] = useState<Rules | null>(null)
  const [error, setError] = useState<React.ReactNode>('')
  const [info, setInfo] = useState('')

  const load = useCallback(async () => {
    try {
      const d = await api<Detail>('GET', `/campaigns/${id}`)
      setDetail(d)
      setRules((r) => r ?? d.campaign.rules)
      const t = await api('GET', `/campaigns/${id}/targets?status=${filter}${numbers ? '&numbers=1' : ''}`)
      setTargets(t.targets)
      setForm(t.form)
    } catch (e) {
      setError(say(e))
    }
  }, [id, filter, numbers])
  useEffect(() => {
    void load()
  }, [load])
  usePoll(load, 4000, detail?.campaign.status === 'running')

  if (!detail || !rules) return error ? <div className="note bad">{error}</div> : null
  const c = detail.campaign
  const total = Object.values(c.counts).reduce((a, b) => a + b, 0)
  const editable = c.status === 'draft' || c.status === 'paused'

  async function start() {
    setError('')
    setInfo('')
    try {
      if (editable && JSON.stringify(rules) !== JSON.stringify(c.rules)) await api('PUT', `/campaigns/${id}`, { rules })
      const d = await api('POST', `/campaigns/${id}/start`)
      if (!d.calling_now) setInfo(`Сейчас не время звонков: обзвон начнёт звонить ${when(d.next_window)}.`)
      await load()
    } catch (e) {
      if (e instanceof ApiError && e.code === 'test_call_needed')
        setError(
          <>
            {say(e)} <Link to={`/agents/${c.agent_id}`}>Открыть агента</Link>
          </>,
        )
      else setError(say(e))
    }
  }
  async function pause() {
    await api('POST', `/campaigns/${id}/pause`).catch((e) => setError(say(e)))
    await load()
  }
  async function saveRules() {
    setError('')
    try {
      await api('PUT', `/campaigns/${id}`, { rules })
      await load()
      setInfo('Правила сохранены.')
    } catch (e) {
      setError(say(e))
    }
  }

  return (
    <>
      <div className="head">
        <div>
          <div className="muted small">
            <Link to="/campaigns">Обзвоны</Link> /
          </div>
          <h1>{c.name}</h1>
          <div className="row">
            <span className={`tag ${c.status === 'running' ? 'good' : c.status === 'paused' ? 'warn' : ''}`}>{campaignStatus[c.status]}</span>
            <span className="muted">
              <Link to={`/agents/${c.agent_id}`}>{c.agent_name}</Link>, версия {c.version_n} · {c.list_name}
              {c.portion > 0 && <> · порция {c.portion}</>}
            </span>
          </div>
        </div>
        <div className="actions">
          <a className="btn" href={`/api/campaigns/${c.id}/export.xlsx`}>
            Скачать Excel
          </a>
          {c.status === 'running' && <button onClick={pause}>Пауза</button>}
          {editable && (
            <button className="primary" onClick={start}>
              {c.status === 'draft' ? 'Запустить' : 'Продолжить'}
            </button>
          )}
        </div>
      </div>
      {error && <div className="note bad">{error}</div>}
      {info && <div className="note">{info}</div>}
      {c.status === 'paused' && <div className="note warn">{pauseReason[c.pause_reason] ?? `Остановлен: ${c.pause_reason}.`}</div>}
      {c.status === 'draft' && !c.version_accepted && (
        <div className="note warn">
          Обзвон не запустится, пока у версии {c.version_n} агента нет принятого тестового звонка. <Link to={`/agents/${c.agent_id}`}>Провести тест</Link>
        </div>
      )}
      <div className="card">
        <div className="grid cols-3">
          <div className="stat">
            <b>
              {(c.counts.done ?? 0) + (c.counts.failed ?? 0) + (c.counts.skipped ?? 0)} / {total}
            </b>
            <span>человек пройдено</span>
          </div>
          <div className="stat">
            <b>{detail.calls.placed ? Math.round((100 * detail.calls.answered) / detail.calls.placed) + '%' : '—'}</b>
            <span>
              звонков с ответом ({detail.calls.answered} из {detail.calls.placed})
            </span>
          </div>
          <div className="stat">
            <b>{seconds(detail.calls.seconds) || '0:00'}</b>
            <span>разговоров, списано {detail.calls.charged_g.toFixed(1)}</span>
          </div>
        </div>
        {Object.keys(detail.results).length > 0 && (
          <div className="row" style={{ marginTop: 14 }}>
            {Object.entries(detail.results)
              .sort((a, b) => b[1] - a[1])
              .map(([k, n]) => (
                <span className="tag" key={k}>
                  {result[k] ?? k}: {n}
                </span>
              ))}
          </div>
        )}
      </div>
      <div className="card">
        <details open={c.status === 'draft'}>
          <summary>
            <b>Правила обзвона</b>{' '}
            <span className="muted small">
              {c.rules.from}–{c.rules.to}, до {c.rules.max_attempts} попыток, порции {c.rules.first_portion} и {c.rules.portion}
            </span>
          </summary>
          <div style={{ marginTop: 12 }}>
            <RulesEditor rules={rules} onChange={setRules} disabled={!editable} />
            {editable && (
              <button style={{ marginTop: 12 }} onClick={saveRules}>
                Сохранить правила
              </button>
            )}
            {!editable && c.status === 'running' && <div className="muted small" style={{ marginTop: 8 }}>Правила меняются на паузе.</div>}
          </div>
        </details>
      </div>
      {detail.form.some((f) => f.answered > 0) && (
        <div className="card">
          <h2>Ответы</h2>
          <div className="scroll">
            <table>
              <thead>
                <tr>
                  <th>Вопрос</th>
                  <th>Ответили</th>
                  <th>Что ответили</th>
                </tr>
              </thead>
              <tbody>
                {detail.form.map((f) => (
                  <tr key={f.key}>
                    <td>{f.title}</td>
                    <td className="num">{f.answered}</td>
                    <td>
                      {f.average != null && <span className="tag">в среднем {f.average.toFixed(1)}</span>}{' '}
                      {(f.values ?? []).map((v) => (
                        <span className="tag" key={v.value}>
                          {v.value}: {v.n}
                          {f.answered > 0 && <span className="muted"> · {Math.round((100 * v.n) / f.answered)}%</span>}
                        </span>
                      ))}
                      {!f.values?.length && f.average == null && f.answered > 0 && <span className="muted small">текстовые ответы — в таблице ниже и в Excel</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="muted small" style={{ marginTop: 8 }}>По последнему звонку каждого человека. Форму заполняет Gemini (если добавлен ключ), ваш ассистент или вы сами на странице звонка.</div>
        </div>
      )}
      <CrmRulesCard campaign={c} editable={editable} onSaved={load} />
      <div className="card">
        <div className="spread" style={{ marginBottom: 10 }}>
          <h2 style={{ margin: 0 }}>Результаты</h2>
          <div className="row">
            <select value={filter} onChange={(e) => setFilter(e.target.value)} style={{ width: 'auto' }} aria-label="Фильтр">
              <option value="">Все</option>
              {Object.entries(targetStatus).map(([k, v]) => (
                <option key={k} value={k}>
                  {v} ({c.counts[k] ?? 0})
                </option>
              ))}
            </select>
            <label className="check">
              <input type="checkbox" checked={numbers} onChange={(e) => setNumbers(e.target.checked)} /> номера целиком
            </label>
          </div>
        </div>
        <div className="scroll">
          <table>
            <thead>
              <tr>
                <th>Номер</th>
                <th>Имя</th>
                <th>Состояние</th>
                <th>Итог</th>
                <th>Попыток</th>
                {form.map((f) => (
                  <th key={f.key}>{f.title}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {targets.map((t) => (
                <tr key={t.id} className={t.call_id ? 'link' : ''} onClick={() => t.call_id && go(`/calls/${t.call_id}`)}>
                  <td className="mono">{t.phone}</td>
                  <td>{t.vars.client_name}</td>
                  <td>
                    {targetStatus[t.status]}
                    {t.status === 'pending' && t.next_at && <div className="muted small">повтор {when(t.next_at)}</div>}
                  </td>
                  <td>{result[t.result] ?? t.result}</td>
                  <td className="num">{t.attempts}</td>
                  {form.map((f) => (
                    <td key={f.key}>{t.answers?.[f.key]?.value != null ? String(t.answers[f.key].value) : ''}</td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {targets.length === 0 && <div className="empty">Никого с таким состоянием.</div>}
        {targets.length === 200 && <div className="muted small">Показаны первые 200 строк; все строки есть в файле Excel.</div>}
      </div>
    </>
  )
}
