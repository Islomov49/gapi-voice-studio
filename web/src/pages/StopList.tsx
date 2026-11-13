import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api'
import { say, when } from '../ru'

type Entry = { phone: string; reason: string; source: string; call_id: string | null; created_at: string }
const sources: Record<string, string> = { manual: 'Добавлен вручную', call: 'Попросил в разговоре', upload: 'Загружен' }

export default function StopList() {
  const [entries, setEntries] = useState<Entry[] | null>(null)
  const [text, setText] = useState('')
  const [message, setMessage] = useState('')
  const load = useCallback(() => api('GET', '/stoplist').then((d) => setEntries(d.stoplist)), [])
  useEffect(() => {
    void load()
  }, [load])

  async function add(e: React.FormEvent) {
    e.preventDefault()
    const phones = text.split(/[\n,;]+/).map((s) => s.trim()).filter(Boolean)
    if (!phones.length) return
    try {
      const d = await api('POST', '/stoplist', { phones, reason: 'добавлен вручную' })
      setMessage(`Добавлено: ${d.added}` + (d.invalid ? `, не распознано: ${d.invalid}` : ''))
      setText('')
      await load()
    } catch (err) {
      setMessage(say(err))
    }
  }
  async function remove(phone: string) {
    if (!confirm(`Убрать ${phone} из стоп-листа? Этому номеру снова можно будет звонить.`)) return
    await api('DELETE', `/stoplist/${encodeURIComponent(phone)}`)
    await load()
  }

  return (
    <>
      <div className="head">
        <div>
          <h1>Стоп-лист</h1>
          <div className="muted">Номера, которым проект не звонит. Просьба «не звоните» в разговоре добавляет номер сама; убрать номер может только человек.</div>
        </div>
      </div>
      <form className="card" onSubmit={add}>
        <label>
          Добавить номера: по одному в строке или через запятую
          <textarea rows={3} value={text} onChange={(e) => setText(e.target.value)} placeholder="+998901234567" />
        </label>
        <div className="row">
          <button className="primary">Добавить</button>
          <span className="muted small">{message}</span>
        </div>
      </form>
      <div className="card">
        {entries && entries.length === 0 && <div className="empty">Стоп-лист пуст.</div>}
        {entries && entries.length > 0 && (
          <table>
            <thead>
              <tr>
                <th>Номер</th>
                <th>Откуда</th>
                <th>Когда</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {entries.map((e) => (
                <tr key={e.phone}>
                  <td className="mono">{e.phone}</td>
                  <td>{e.call_id ? <Link to={`/calls/${e.call_id}`}>{sources[e.source]}</Link> : sources[e.source] ?? e.source}</td>
                  <td className="muted">{when(e.created_at)}</td>
                  <td style={{ textAlign: 'right' }}>
                    <button className="quiet danger" onClick={() => remove(e.phone)}>
                      Убрать
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  )
}
