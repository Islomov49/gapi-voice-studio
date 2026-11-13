import { useCallback, useEffect, useRef, useState } from 'react'
import { api, ApiError, type ContactList } from '../api'
import { say, when } from '../ru'

export default function Lists() {
  const [lists, setLists] = useState<ContactList[] | null>(null)
  const [error, setError] = useState('')
  const [headers, setHeaders] = useState<string[] | null>(null)
  const [busy, setBusy] = useState(false)
  const [last, setLast] = useState<ContactList | null>(null)
  const file = useRef<HTMLInputElement>(null)
  const load = useCallback(() => api('GET', '/lists').then((d) => setLists(d.lists)), [])
  useEffect(() => {
    void load()
  }, [load])

  async function upload(phoneColumn?: string) {
    const f = file.current?.files?.[0]
    if (!f) return
    const body = new FormData()
    body.append('file', f)
    if (phoneColumn) body.append('phone_column', phoneColumn)
    setBusy(true)
    setError('')
    try {
      const d = await api<ContactList>('POST', '/lists', body)
      setLast(d)
      setHeaders(null)
      if (file.current) file.current.value = ''
      await load()
    } catch (e) {
      if (e instanceof ApiError && e.code === 'phone_column_needed') setHeaders(e.extra.headers as string[])
      else setError(say(e))
    } finally {
      setBusy(false)
    }
  }
  async function archive(id: string) {
    if (!confirm('Убрать список? Обзвоны, которые по нему уже созданы, останутся.')) return
    await api('DELETE', `/lists/${id}`)
    await load()
  }

  return (
    <>
      <div className="head">
        <div>
          <h1>Контакты</h1>
          <div className="muted">Списки для обзвона из Excel или CSV. Первая строка файла — названия колонок; остальные колонки становятся переменными сценария.</div>
        </div>
      </div>
      <div className="card">
        <h2>Загрузить список</h2>
        <div className="row">
          <input ref={file} type="file" accept=".xlsx,.csv" style={{ maxWidth: 360 }} onChange={() => setHeaders(null)} />
          <button className="primary" disabled={busy} onClick={() => upload()}>
            Загрузить
          </button>
        </div>
        {headers && (
          <div className="note warn" style={{ marginTop: 12 }}>
            Не удалось понять, в какой колонке номера. Выберите её:
            <div className="row" style={{ marginTop: 8 }}>
              {headers.filter(Boolean).map((h) => (
                <button key={h} onClick={() => upload(h)}>
                  {h}
                </button>
              ))}
            </div>
          </div>
        )}
        {error && (
          <div className="note bad" style={{ marginTop: 12 }}>
            {error}
          </div>
        )}
        {last && (
          <div className="note good" style={{ marginTop: 12 }}>
            «{last.name}»: принято {last.stats.accepted} из {last.stats.rows} строк. Дублей: {last.stats.duplicates}, без верного номера: {last.stats.invalid}, в стоп-листе:{' '}
            {last.stats.stopped}. Номера взяты из колонки «{last.stats.phone_column}».
            {last.columns.length > 0 && (
              <div>
                Переменные для сценария:{' '}
                {last.columns.map((c) => (
                  <span key={c.key} className="mono">
                    {`{{${c.key}}} `}
                  </span>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
      <div className="card">
        {lists && lists.length === 0 && <div className="empty">Списков пока нет.</div>}
        {lists && lists.length > 0 && (
          <table>
            <thead>
              <tr>
                <th>Список</th>
                <th>Контактов</th>
                <th>Колонки</th>
                <th>Загружен</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {lists.map((l) => (
                <tr key={l.id}>
                  <td>{l.name}</td>
                  <td className="num">{l.stats.accepted}</td>
                  <td className="muted small">{l.columns.map((c) => c.title).join(', ')}</td>
                  <td className="muted">{when(l.created_at)}</td>
                  <td style={{ textAlign: 'right' }}>
                    <button className="quiet danger" onClick={() => archive(l.id)}>
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
