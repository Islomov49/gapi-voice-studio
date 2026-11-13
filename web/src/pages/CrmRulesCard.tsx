import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api, type Campaign, type CrmConnection, type CrmRules, type Pipeline, type StageRef } from '../api'
import { say } from '../ru'
import { crmKinds, entityName, stageKey, usePipelines } from './Crm'

// The results a person may end with, in the three groups a business thinks in.
const groups = [
  { id: 'yes', title: 'Согласие или вопрос решён', results: ['agreed', 'resolved'] },
  { id: 'no', title: 'Отказ или просьба не звонить', results: ['declined', 'lost_interest', 'do_not_call'] },
  { id: 'other', title: 'Не дозвонились и всё остальное', results: ['*'] },
]

function keyOf(pipelines: Pipeline[], ref?: StageRef) {
  if (!ref) return ''
  const p = pipelines.find((p) => p.id === ref.pipeline_id && p.stages.some((s) => s.id === ref.stage_id))
  return p ? stageKey(p, ref.stage_id) : ''
}

function StageSelect({ pipelines, value, onChange, none }: { pipelines: Pipeline[]; value: string; onChange: (v: string) => void; none: string }) {
  return (
    <select value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">{none}</option>
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
  )
}

// What a campaign does in the CRM: a note per person, the stage by result, a
// task for the card's manager, and new cards from a stage while it runs.
export default function CrmRulesCard({ campaign, editable, onSaved }: { campaign: Campaign; editable: boolean; onSaved: () => void }) {
  const [connections, setConnections] = useState<CrmConnection[]>([])
  const [connection, setConnection] = useState(campaign.crm?.connection_id ?? '')
  const [note, setNote] = useState(campaign.crm?.note ?? true)
  const [move, setMove] = useState<Record<string, string>>({})
  const [taskYes, setTaskYes] = useState(campaign.crm?.task?.agreed ?? '')
  const [taskCallback, setTaskCallback] = useState(campaign.crm?.task?.callback ?? '')
  const [feed, setFeed] = useState('')
  const [message, setMessage] = useState('')
  const [error, setError] = useState('')
  const { pipelines, error: pipelinesError } = usePipelines(connection)

  useEffect(() => {
    api('GET', '/crm').then((d) => setConnections(d.connections))
  }, [])
  // the saved stages show once the pipelines they belong to have loaded
  useEffect(() => {
    if (!pipelines.length || !campaign.crm) return
    const m: Record<string, string> = {}
    for (const g of groups) m[g.id] = keyOf(pipelines, campaign.crm.move?.[g.results[0]])
    setMove(m)
    const f = campaign.crm.feed
    setFeed(f ? keyOf(pipelines, { pipeline_id: f.pipeline_id, stage_id: f.stage_id }) : '')
  }, [pipelines, campaign.crm])

  if (connections.length === 0 && !campaign.crm)
    return (
      <div className="card">
        <b>CRM</b>{' '}
        <span className="muted small">
          Чтобы обзвон писал итог в карточки Bitrix24 или amoCRM, <Link to="/crm">подключите CRM</Link> и соберите список со стадии воронки.
        </span>
      </div>
    )

  async function save() {
    setError('')
    setMessage('')
    let crm: CrmRules | null = null
    if (connection) {
      crm = { connection_id: connection, note, move: {}, task: {} }
      for (const g of groups) {
        const [, pipeline_id, stage_id] = (move[g.id] ?? '').split('|')
        if (stage_id) for (const r of g.results) crm.move![r] = { pipeline_id, stage_id }
      }
      if (taskYes.trim()) for (const r of groups[0].results) crm.task![r] = taskYes.trim()
      if (taskCallback.trim()) crm.task!.callback = taskCallback.trim()
      const [entity, pipeline_id, stage_id] = feed.split('|')
      if (stage_id) crm.feed = { entity, pipeline_id, stage_id }
    }
    try {
      await api('PUT', `/campaigns/${campaign.id}`, { crm })
      setMessage('Сохранено.')
      onSaved()
    } catch (e) {
      setError(say(e))
    }
  }

  const current = connections.find((c) => c.id === campaign.crm?.connection_id)
  return (
    <div className="card">
      <details open={campaign.status === 'draft' && !!campaign.crm}>
        <summary>
          <b>CRM</b>{' '}
          <span className="muted small">
            {campaign.crm ? `итог пишется в ${current ? crmKinds[current.kind] + ' · ' + current.name : 'CRM'}` : 'итог в CRM не пишется'}
            {campaign.crm?.feed && ', новые карточки со стадии подхватываются'}
          </span>
          {campaign.crm_failed > 0 && <span className="tag bad"> не записано: {campaign.crm_failed}</span>}
        </summary>
        <fieldset disabled={!editable} style={{ border: 0, padding: 0, margin: '12px 0 0' }}>
          <div className="grid cols-2">
            <label>
              Куда писать итог
              <select value={connection} onChange={(e) => setConnection(e.target.value)}>
                <option value="">Не писать в CRM</option>
                {connections.map((c) => (
                  <option key={c.id} value={c.id}>
                    {crmKinds[c.kind]} · {c.name}
                  </option>
                ))}
              </select>
            </label>
            <label className="check" style={{ alignSelf: 'end', marginBottom: 12 }}>
              <input type="checkbox" checked={note} onChange={(e) => setNote(e.target.checked)} /> комментарий в карточку, когда звонки человеку закончены
            </label>
          </div>
          {connection && (
            <>
              <h3>Перевести карточку на стадию</h3>
              <div className="grid cols-3">
                {groups.map((g) => (
                  <label key={g.id}>
                    {g.title}
                    <StageSelect pipelines={pipelines} value={move[g.id] ?? ''} onChange={(v) => setMove({ ...move, [g.id]: v })} none="Не переводить" />
                  </label>
                ))}
              </div>
              <h3>Задача ответственному за карточку</h3>
              <div className="grid cols-2">
                <label>
                  При согласии
                  <input value={taskYes} onChange={(e) => setTaskYes(e.target.value)} placeholder="Например: клиент согласен, перезвонить и оформить" />
                </label>
                <label>
                  Если просил перезвонить
                  <input value={taskCallback} onChange={(e) => setTaskCallback(e.target.value)} placeholder="Например: перезвонить клиенту лично" />
                </label>
              </div>
              <label>
                Подхватывать новые карточки со стадии, пока обзвон идёт
                <StageSelect pipelines={pipelines} value={feed} onChange={setFeed} none="Не подхватывать" />
              </label>
              <p className="muted small">
                Studio раз в минуту смотрит стадию и добавляет в обзвон людей из новых карточек. Комментарий, стадия и задача пишутся, когда звонки человеку закончены:
                разговор состоялся или попытки исчерпаны.
              </p>
            </>
          )}
        </fieldset>
        {(error || pipelinesError) && <div className="note bad">{error || pipelinesError}</div>}
        {editable ? (
          <div className="row">
            <button onClick={save}>Сохранить</button>
            <span className="muted small">{message}</span>
          </div>
        ) : (
          <div className="muted small">Меняется в черновике и на паузе.</div>
        )}
      </details>
    </div>
  )
}
