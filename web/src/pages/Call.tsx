import { useCallback, useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api, type Call } from '../api'
import { usePoll } from '../App'
import { callStatus, result, say, seconds, when } from '../ru'

const who: Record<string, string> = { agent: 'Агент', caller: 'Абонент' }

// One call: the recording, the text turn by turn, what the checks found, the
// form; for a test call, the tester's verdict.
export default function CallPage() {
  const { id } = useParams()
  const [call, setCall] = useState<Call | null>(null)
  const [feedback, setFeedback] = useState('')
  const [error, setError] = useState('')
  const [edits, setEdits] = useState<Record<string, string>>({})

  const load = useCallback(
    () =>
      api<Call>('GET', `/calls/${id}`)
        .then(setCall)
        .catch((e) => setError(say(e))),
    [id],
  )
  useEffect(() => {
    void load()
  }, [load])
  usePoll(load, 2500, !!call && !call.settled)

  if (!call) return error ? <div className="note bad">{error}</div> : null

  async function verdict(accepted: boolean) {
    setError('')
    try {
      await api('POST', `/calls/${id}/verdict`, { accepted, feedback })
      await load()
    } catch (e) {
      setError(say(e))
    }
  }
  async function saveAnswers() {
    try {
      await api('PUT', `/calls/${id}/answers`, edits)
      setEdits({})
      await load()
    } catch (e) {
      setError(say(e))
    }
  }
  async function hangUp() {
    await api('DELETE', `/calls/${id}`).catch(() => {})
    await load()
  }

  const talked = call.status === 'ended'
  const state = call.settled ? (talked ? result[call.outcome] ?? callStatus.ended : result[call.end_reason] ?? callStatus.failed) : callStatus[call.status]
  const turns = call.transcript ?? []
  const form = call.form ?? []
  const answers = call.answers ?? {}

  return (
    <>
      <div className="head">
        <div>
          <div className="muted small">
            {call.campaign_id ? <Link to={`/campaigns/${call.campaign_id}`}>Обзвон</Link> : <Link to={`/agents`}>Агенты</Link>} /
          </div>
          <h1>{call.kind === 'test' ? 'Тестовый звонок' : 'Звонок'}</h1>
          <div className="row">
            <span className={`tag ${!call.settled ? 'warn' : talked ? 'good' : ''}`}>{state}</span>
            <span className="muted">
              {call.agent_name}, версия {call.version_n} · <span className="mono">{call.phone}</span> · {when(call.created_at)}
              {call.duration_seconds != null && <> · {seconds(call.duration_seconds)}</>}
            </span>
          </div>
        </div>
        {!call.settled && (
          <div className="actions">
            <button className="danger" onClick={hangUp}>
              Завершить звонок
            </button>
          </div>
        )}
      </div>
      {error && <div className="note bad">{error}</div>}
      <div className="split">
        <div className="card">
          <h2>Разговор</h2>
          {call.has_recording && call.settled && <audio controls preload="none" src={`/api/calls/${call.id}/recording`} />}
          {!call.settled && <div className="note">Звонок ещё идёт. Текст и запись появятся через несколько секунд после его окончания.</div>}
          {call.settled && turns.length === 0 && <div className="empty">{talked ? 'Текст разговора не получен.' : 'Разговора не было.'}</div>}
          <div className="turns">
            {turns.map((t, i) =>
              t.role === 'agent' || t.role === 'caller' ? (
                <div key={i} className={`turn ${t.role}`}>
                  <div className="who">{who[t.role]}</div>
                  {t.text}
                </div>
              ) : (
                <div key={i} className="turn event">
                  {t.role === 'function' ? `функция ${t.name}` : t.text}
                </div>
              ),
            )}
          </div>
        </div>
        <div>
          {call.settled && talked && (
            <div className="card">
              <h2>Проверки</h2>
              <div className={`note ${call.checks.ai_disclosed ? 'good' : 'bad'}`}>
                {call.checks.ai_disclosed ? 'Агент в первых словах назвал себя голосовым ассистентом.' : 'Агент не назвал себя голосовым ассистентом в первых словах.'}
              </div>
              {call.checks.do_not_call && <div className="note warn">Абонент попросил больше не звонить: номер добавлен в стоп-лист.</div>}
            </div>
          )}
          {call.kind === 'test' && call.settled && (
            <div className="card">
              <h2>Ваша оценка</h2>
              {call.accepted === true && <div className="note good">Звонок принят: этой версией агента можно запускать обзвон.</div>}
              {call.accepted === false && <div className="note bad">Замечания: {call.feedback || 'без текста'}</div>}
              {call.accepted === null && (
                <>
                  <label>
                    Что не так (если есть замечания)
                    <textarea rows={3} value={feedback} onChange={(e) => setFeedback(e.target.value)} placeholder="Например: слишком быстро перешёл к оценке, не дослушал" />
                  </label>
                  <div className="row">
                    <button className="primary" disabled={!talked} onClick={() => verdict(true)}>
                      Принять звонок
                    </button>
                    <button onClick={() => verdict(false)}>Есть замечания</button>
                  </div>
                  {!talked && <div className="muted small" style={{ marginTop: 8 }}>Принять можно только состоявшийся разговор.</div>}
                </>
              )}
              <div style={{ marginTop: 12 }}>
                <Link to={`/agents`}>К агентам</Link>
              </div>
            </div>
          )}
          {form.length > 0 && call.settled && talked && (
            <div className="card">
              <h2>Форма</h2>
              {Object.keys(answers).length === 0 && (
                <div className="note small">Форму заполняет ваш ассистент через MCP (действие fill_call_form) или вы сами здесь.</div>
              )}
              {form.map((f) => {
                const a = answers[f.key]
                return (
                  <label key={f.key}>
                    {f.title} {a?.edited && <span className="tag">исправлено</span>}
                    <input value={edits[f.key] ?? (a?.value != null ? String(a.value) : '')} onChange={(e) => setEdits({ ...edits, [f.key]: e.target.value })} />
                    {a?.quote && <span className="muted small">«{a.quote}»</span>}
                  </label>
                )
              })}
              <button disabled={Object.keys(edits).length === 0} onClick={saveAnswers}>
                Сохранить ответы
              </button>
            </div>
          )}
        </div>
      </div>
    </>
  )
}
