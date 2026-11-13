import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, type ContactList, type CrmConnection, type Pipeline } from '../api'
import { say, when } from '../ru'

export const crmKinds: Record<string, string> = { bitrix24: 'Bitrix24', amocrm: 'amoCRM' }

// usePipelines loads a connection's funnels once it is chosen.
export function usePipelines(connection: string) {
  const [pipelines, setPipelines] = useState<Pipeline[]>([])
  const [error, setError] = useState('')
  useEffect(() => {
    setPipelines([])
    setError('')
    if (!connection) return
    api('GET', `/crm/${connection}/pipelines`)
      .then((d) => setPipelines(d.pipelines))
      .catch((e) => setError(say(e)))
  }, [connection])
  return { pipelines, error }
}

// stageKey names a stage of a pipeline in one string, for a <select>.
export const stageKey = (p: Pipeline, stageId: string) => `${p.entity}|${p.id}|${stageId}`
export const entityName: Record<string, string> = { deal: 'Сделки', lead: 'Лиды' }

export default function Crm() {
  const [connections, setConnections] = useState<CrmConnection[] | null>(null)
  const [kind, setKind] = useState('bitrix24')
  const [webhook, setWebhook] = useState('')
  const [account, setAccount] = useState('')
  const [token, setToken] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [from, setFrom] = useState('')
  const [stage, setStage] = useState('')
  const [name, setName] = useState('')
  const [imported, setImported] = useState<ContactList | null>(null)
  const [importError, setImportError] = useState('')
  const { pipelines, error: pipelinesError } = usePipelines(from)

  const load = useCallback(
    () =>
      api('GET', '/crm').then((d) => {
        setConnections(d.connections)
        setFrom((f) => f || d.connections[0]?.id || '')
      }),
    [],
  )
  useEffect(() => {
    void load()
  }, [load])

  async function connect(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await api('POST', '/crm', kind === 'bitrix24' ? { kind, webhook } : { kind, account, token })
      setWebhook('')
      setToken('')
      await load()
    } catch (err) {
      setError(say(err))
    } finally {
      setBusy(false)
    }
  }
  async function remove(id: string) {
    if (!confirm('Отключить эту CRM? Обзвоны перестанут писать в неё результаты.')) return
    try {
      await api('DELETE', `/crm/${id}`)
      await load()
    } catch (err) {
      setError(say(err))
    }
  }
  async function importStage() {
    const [entity, pipeline_id, stage_id] = stage.split('|')
    setBusy(true)
    setImportError('')
    setImported(null)
    try {
      setImported(await api<ContactList>('POST', `/crm/${from}/import`, { entity, pipeline_id, stage_id, name }))
    } catch (err) {
      setImportError(say(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <div className="head">
        <div>
          <h1>CRM</h1>
          <div className="muted">
            Studio берёт людей со стадии воронки и пишет итог звонка в карточку. Она сама обращается к CRM, поэтому публичный адрес ей не нужен.
          </div>
        </div>
      </div>
      <div className="grid cols-2">
        <form className="card" onSubmit={connect}>
          <h2>Подключить</h2>
          <label>
            CRM
            <select value={kind} onChange={(e) => setKind(e.target.value)}>
              <option value="bitrix24">Bitrix24</option>
              <option value="amocrm">amoCRM</option>
            </select>
          </label>
          {kind === 'bitrix24' ? (
            <>
              <label>
                Адрес входящего вебхука
                <input type="password" value={webhook} onChange={(e) => setWebhook(e.target.value)} placeholder="https://<портал>/rest/<пользователь>/<ключ>/" autoComplete="off" />
              </label>
              <p className="muted small">
                В Bitrix24: «Разработчикам» → «Другое» → «Входящий вебхук», право «CRM». Скопируйте адрес вебхука целиком. Вебхук действует от имени создавшего его
                сотрудника.
              </p>
            </>
          ) : (
            <>
              <label>
                Адрес аккаунта
                <input value={account} onChange={(e) => setAccount(e.target.value)} placeholder="mycompany.amocrm.ru" />
              </label>
              <label>
                Долгосрочный токен
                <input type="password" value={token} onChange={(e) => setToken(e.target.value)} autoComplete="off" />
              </label>
              <p className="muted small">В amoCRM: «amoМаркет» → «Создать интеграцию» (внешняя) → вкладка «Ключи и доступы» → «Сгенерировать долгосрочный токен».</p>
            </>
          )}
          {error && <div className="note bad">{error}</div>}
          <button className="primary" disabled={busy}>
            Проверить и подключить
          </button>
          <p className="muted small" style={{ marginTop: 10 }}>
            Ключ проверяется запросом в CRM и хранится зашифрованным в базе Studio на этой машине.
          </p>
        </form>
        <div className="card">
          <h2>Подключённые</h2>
          {connections && connections.length === 0 && <div className="empty">Пока ни одной.</div>}
          {connections && connections.length > 0 && (
            <table>
              <tbody>
                {connections.map((c) => (
                  <tr key={c.id}>
                    <td>
                      <b>{crmKinds[c.kind]}</b>
                      <div className="muted small">{c.name}</div>
                    </td>
                    <td className="muted small">
                      {c.base.replace('https://', '')}
                      <div>{when(c.created_at)}</div>
                    </td>
                    <td style={{ textAlign: 'right' }}>
                      <button className="quiet danger" onClick={() => remove(c.id)}>
                        Отключить
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>
      {connections && connections.length > 0 && (
        <div className="card">
          <h2>Список со стадии воронки</h2>
          <p className="muted small">
            Карточки, которые стоят на стадии сейчас, становятся списком контактов. Каждый контакт помнит свою карточку, и обзвон по такому списку может писать в неё
            итог.
          </p>
          <div className="grid cols-3">
            <label>
              CRM
              <select value={from} onChange={(e) => setFrom(e.target.value)}>
                {connections.map((c) => (
                  <option key={c.id} value={c.id}>
                    {crmKinds[c.kind]} · {c.name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Стадия
              <select value={stage} onChange={(e) => setStage(e.target.value)}>
                <option value="">{pipelines.length ? 'Выберите' : 'Загрузка…'}</option>
                {pipelines.map((p) => (
                  <optgroup key={p.entity + p.id} label={`${entityName[p.entity]} · ${p.name}`}>
                    {p.stages.map((s) => (
                      <option key={s.id} value={stageKey(p, s.id)}>
                        {s.name}
                      </option>
                    ))}
                  </optgroup>
                ))}
              </select>
            </label>
            <label>
              Название списка
              <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Например, новые заявки" />
            </label>
          </div>
          {(pipelinesError || importError) && <div className="note bad">{pipelinesError || importError}</div>}
          {imported && (
            <div className="note good">
              «{imported.name}»: {imported.stats.accepted} человек из {imported.stats.rows} карточек; без номера: {imported.stats.invalid}, дублей:{' '}
              {imported.stats.duplicates}. Теперь <Link to="/campaigns">создайте обзвон</Link> по этому списку и включите в нём запись итога в CRM.
            </div>
          )}
          <button className="primary" disabled={busy || !stage} onClick={importStage}>
            Собрать список
          </button>
        </div>
      )}
    </>
  )
}
