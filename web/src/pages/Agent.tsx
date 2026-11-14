import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, type AgentDetail, type Call, type Field, type Problem, type Spec } from '../api'
import { usePoll, useSession } from '../App'
import { callStatus, fieldTypes, result, say, seconds, when } from '../ru'

const where: Record<string, string> = { company: 'Компания', script: 'Сценарий', voice: 'Голос', variables: 'Переменные', vocabulary: 'Словарь', max_call_seconds: 'Длина звонка' }

function FormEditor({ form, onChange }: { form: Field[]; onChange: (f: Field[]) => void }) {
  const set = (i: number, patch: Partial<Field>) => onChange(form.map((f, j) => (j === i ? { ...f, ...patch } : f)))
  return (
    <>
      {form.length === 0 && <div className="muted small">Полей пока нет. Форма — это то, что звонок должен выяснить: оценка, причина, удобное время.</div>}
      {form.map((f, i) => (
        <div className="field" key={i}>
          <input value={f.title} onChange={(e) => set(i, { title: e.target.value })} placeholder="Название колонки" aria-label="Название" />
          <select value={f.type} onChange={(e) => set(i, { type: e.target.value })} aria-label="Тип">
            {Object.entries(fieldTypes).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
          <input className="mono" value={f.key} onChange={(e) => set(i, { key: e.target.value })} placeholder="key" aria-label="Ключ" />
          <button className="quiet danger" onClick={() => onChange(form.filter((_, j) => j !== i))} aria-label="Удалить поле">
            ✕
          </button>
          {f.type === 'choice' && (
            <input
              className="wide"
              value={(f.options ?? []).join(', ')}
              onChange={(e) => set(i, { options: e.target.value.split(',').map((s) => s.trim()).filter(Boolean) })}
              placeholder="Варианты через запятую"
            />
          )}
          <input className="wide" value={f.ask ?? ''} onChange={(e) => set(i, { ask: e.target.value })} placeholder="Как агент спрашивает об этом (на языке звонка). Пусто — агент не спрашивает отдельно" />
          <label className="check wide">
            <input type="checkbox" checked={!!f.required} onChange={(e) => set(i, { required: e.target.checked })} /> обязательное
          </label>
        </div>
      ))}
      <button onClick={() => onChange([...form, { key: `field_${form.length + 1}`, title: '', type: 'text' }])}>Добавить поле</button>
    </>
  )
}

export default function AgentPage() {
  const { id } = useParams()
  const { me, reload } = useSession()
  const go = useNavigate()
  const [detail, setDetail] = useState<AgentDetail | null>(null)
  const [name, setName] = useState('')
  const [draft, setDraft] = useState<Spec | null>(null)
  const [problems, setProblems] = useState<Problem[]>([])
  const [variables, setVariables] = useState<string[]>([])
  const [prompt, setPrompt] = useState('')
  const [calls, setCalls] = useState<Call[]>([])
  const [values, setValues] = useState<Record<string, string>>({})
  const [phone, setPhone] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const take = useCallback((d: AgentDetail) => {
    setDetail(d)
    setName(d.agent.name)
    setDraft(d.latest.spec)
    setProblems(d.problems)
    setVariables(d.variables)
    setPrompt(d.prompt)
  }, [])
  useEffect(() => {
    api<AgentDetail>('GET', `/agents/${id}`).then(take).catch((e) => setError(say(e)))
  }, [id, take])
  const loadCalls = useCallback(() => api('GET', `/calls?kind=test&version_id=${id}&limit=8`).then((d) => setCalls(d.calls)), [id])
  useEffect(() => {
    void loadCalls()
  }, [loadCalls])
  usePoll(loadCalls, 3000, calls.some((c) => !c.settled))

  const dirty = useMemo(() => !!detail && !!draft && JSON.stringify(draft) !== JSON.stringify(detail.latest.spec), [detail, draft])

  // the checks follow the typing, a little behind it
  const timer = useRef<number | undefined>(undefined)
  useEffect(() => {
    if (!draft || !dirty) return
    clearTimeout(timer.current)
    timer.current = window.setTimeout(() => {
      api('POST', '/check', { spec: draft }).then((d) => {
        setProblems(d.problems)
        setVariables(d.variables)
        setPrompt(d.prompt)
      })
    }, 600)
    return () => clearTimeout(timer.current)
  }, [draft, dirty])

  if (!detail || !draft) return error ? <div className="note bad">{error}</div> : null
  const set = (patch: Partial<Spec>) => setDraft({ ...draft, ...patch })
  const latest = detail.latest

  async function save() {
    setBusy(true)
    setError('')
    try {
      if (name.trim() !== detail!.agent.name) await api('PUT', `/agents/${id}`, { name })
      take(await api<AgentDetail>('POST', `/agents/${id}/versions`, { spec: draft }))
    } catch (e) {
      setError(say(e))
    } finally {
      setBusy(false)
    }
  }
  async function testCall() {
    setBusy(true)
    setError('')
    try {
      if (phone.trim() && !me.workspace.tester_phone) {
        await api('PUT', '/workspace', { tester_phone: phone })
        await reload()
      }
      await api('POST', '/calls/test', { version_id: latest.id, phone: phone.trim() || undefined, values })
      await loadCalls()
    } catch (e) {
      setError(say(e))
    } finally {
      setBusy(false)
    }
  }
  async function archive() {
    if (!confirm('Убрать агента в архив?')) return
    try {
      await api('DELETE', `/agents/${id}`)
      go('/agents')
    } catch (e) {
      setError(say(e))
    }
  }

  const canCall = !dirty && problems.length === 0 && (me.lines ?? []).length > 0
  return (
    <>
      <div className="head">
        <div>
          <div className="muted small">
            <Link to="/agents">Агенты</Link> /
          </div>
          <h1>{detail.agent.name}</h1>
          <div className="row">
            <span className="tag">Версия {latest.n}</span>
            {latest.accepted_at ? <span className="tag good">Тестовый звонок принят</span> : <span className="tag warn">Нужен тестовый звонок</span>}
            {dirty && <span className="tag warn">Есть несохранённые правки</span>}
          </div>
        </div>
        <div className="actions">
          <button className="quiet danger" onClick={archive}>
            В архив
          </button>
          <button className="primary" disabled={busy || (!dirty && name.trim() === detail.agent.name)} onClick={save}>
            Сохранить
          </button>
        </div>
      </div>
      {error && <div className="note bad">{error}</div>}
      {dirty && latest.accepted_at && <div className="note warn">Сохранение создаст новую версию. Для неё снова понадобится тестовый звонок, прежде чем запускать обзвон.</div>}
      <div className="split">
        <div>
          <div className="card">
            <div className="grid cols-2">
              <label>
                Название агента
                <input value={name} onChange={(e) => setName(e.target.value)} />
              </label>
              <label>
                Компания, как её называют вслух
                <input value={draft.company} onChange={(e) => set({ company: e.target.value })} placeholder="Namuna Mebel" />
              </label>
            </div>
            <div className="note small">
              Первые слова агента в каждом звонке задаёт Studio: «Assalomu alaykum! Men «{draft.company || '…'}» kompaniyasining sun'iy intellekt yordamchisiman.» В сценарии
              это место обозначено как <span className="mono">{'{{studio_greeting}}'}</span>.
            </div>
            <label>
              Сценарий
              <textarea className="script" value={draft.script} onChange={(e) => set({ script: e.target.value })} spellCheck={false} />
            </label>
            <div className="muted small">
              Перепишите под себя первый абзац (кто звонит) и список «Ma'lumotlar» (факты, которые агент может назвать). Чего нет в сценарии, агент не знает. Данные из
              списка контактов подставляются как <span className="mono">{'{{client_name}}'}</span>.
            </div>
            <div className="muted small" style={{ marginTop: 6 }}>
              Строка только для части людей: начните её с <span className="mono">{'{{if segment=клиент}}'}</span>, и она попадёт в инструкцию только тем, у кого в колонке
              списка <span className="mono">segment</span> стоит «клиент» (несколько значений через <span className="mono">|</span>, «не равно» —{' '}
              <span className="mono">!=</span>; несколько условий подряд должны выполняться все). Остальным агент этой строки не получит вовсе.
            </div>
          </div>
          <div className="card">
            <h2>Форма: что выяснить</h2>
            <FormEditor form={draft.form ?? []} onChange={(form) => set({ form })} />
          </div>
          <div className="card">
            <h2>Настройки</h2>
            <div className="grid cols-3">
              <label>
                Голос
                <select value={draft.voice} onChange={(e) => set({ voice: e.target.value })}>
                  <option value="charos">Charos, женский</option>
                  <option value="sarvar">Sarvar, мужской</option>
                </select>
              </label>
              <label>
                Предел звонка, секунд
                <input type="number" min={30} max={1800} value={draft.max_call_seconds} onChange={(e) => set({ max_call_seconds: Number(e.target.value) })} />
              </label>
            </div>
            <label>
              Слова, которые нужно расслышать точно: названия, фамилии, термины (через запятую, до 20)
              <input
                value={(draft.vocabulary ?? []).join(', ')}
                onChange={(e) => set({ vocabulary: e.target.value.split(',').map((s) => s.trimStart()).filter((s, i, a) => s !== '' || i === a.length - 1) })}
              />
            </label>
            {variables.length > 0 && (
              <>
                <h3>Переменные сценария и значения по умолчанию</h3>
                <div className="grid cols-2">
                  {variables.map((v) => (
                    <label key={v}>
                      <span className="mono">{`{{${v}}}`}</span>
                      <input value={draft.variables?.[v] ?? ''} onChange={(e) => set({ variables: { ...(draft.variables ?? {}), [v]: e.target.value } })} />
                    </label>
                  ))}
                </div>
              </>
            )}
          </div>
        </div>
        <div>
          {problems.length > 0 && (
            <div className="card">
              <h2>Что исправить</h2>
              {problems.map((p, i) => (
                <div className="note bad" key={i}>
                  <b>{where[p.field] ?? 'Форма'}:</b> {p.text}
                </div>
              ))}
            </div>
          )}
          <div className="card">
            <h2>Тестовый звонок</h2>
            <p className="muted small">Агент позвонит тестировщику этой версией сценария. Послушайте, как он говорит, и примите звонок или напишите, что не так.</p>
            {(me.lines ?? []).length === 0 && <div className="note warn">В проекте Gapi нет подключённой линии: звонить не с чего.</div>}
            <label>
              Кому звонить
              <input value={phone} onChange={(e) => setPhone(e.target.value)} placeholder={me.workspace.tester_phone || '+998901234567'} inputMode="tel" />
            </label>
            {variables.length > 0 && (
              <details style={{ marginBottom: 12 }}>
                <summary className="small muted">Значения переменных для этого звонка</summary>
                {variables.map((v) => (
                  <label key={v} style={{ marginTop: 8 }}>
                    <span className="mono">{v}</span>
                    <input value={values[v] ?? ''} onChange={(e) => setValues({ ...values, [v]: e.target.value })} placeholder={draft.variables?.[v] ?? ''} />
                  </label>
                ))}
              </details>
            )}
            <button className="primary" disabled={busy || !canCall} onClick={testCall}>
              Позвонить тестировщику
            </button>
            {dirty && <div className="muted small" style={{ marginTop: 8 }}>Сначала сохраните правки.</div>}
            {calls.length > 0 && (
              <table style={{ marginTop: 14 }}>
                <tbody>
                  {calls.map((c) => (
                    <tr key={c.id} className="link" onClick={() => go(`/calls/${c.id}`)}>
                      <td>
                        <Link to={`/calls/${c.id}`}>v{c.version_n}</Link>
                        <div className="muted small">{when(c.created_at)}</div>
                      </td>
                      <td>
                        {c.settled ? (c.status === 'ended' ? callStatus.ended : result[c.end_reason] ?? callStatus.failed) : callStatus[c.status]}
                        <div className="muted small">{seconds(c.duration_seconds)}</div>
                      </td>
                      <td style={{ textAlign: 'right' }}>
                        {c.accepted === true && <span className="tag good">Принят</span>}
                        {c.accepted === false && <span className="tag bad">Замечания</span>}
                        {c.accepted === null && c.settled && c.status === 'ended' && <span className="tag warn">Оцените</span>}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
          <div className="card">
            <details>
              <summary>
                <b>Инструкция целиком</b> <span className="muted small">как её прочитает модель, {prompt.length} знаков из 8000</span>
              </summary>
              <pre className="prompt" style={{ marginTop: 10 }}>
                {prompt}
              </pre>
            </details>
          </div>
          <div className="card">
            <h2>Версии</h2>
            <table>
              <tbody>
                {detail.versions.slice(0, 8).map((v) => (
                  <tr key={v.id}>
                    <td className="num">v{v.n}</td>
                    <td className="muted small">
                      {when(v.created_at)} · {v.author === 'assistant' ? 'ассистент' : 'вручную'}
                      {v.note && <div>{v.note}</div>}
                    </td>
                    <td style={{ textAlign: 'right' }}>{v.accepted_at && <span className="tag good">Принята</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </>
  )
}
