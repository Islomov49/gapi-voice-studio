// The Studio's API as the interface uses it. An answer that is not 2xx is
// thrown as ApiError with the server's code, so a page can switch on it.

export class ApiError extends Error {
  code: string
  status: number
  extra: Record<string, unknown>
  constructor(status: number, body: Record<string, unknown>) {
    super(String(body.hint ?? body.error ?? 'error'))
    this.status = status
    this.code = String(body.error ?? 'error')
    this.extra = body
  }
}

export async function api<T = any>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, credentials: 'same-origin' }
  if (body instanceof FormData) init.body = body
  else if (body !== undefined) {
    init.body = JSON.stringify(body)
    init.headers = { 'Content-Type': 'application/json' }
  }
  const resp = await fetch('/api' + path, init)
  const data = await resp.json().catch(() => ({}))
  if (!resp.ok) throw new ApiError(resp.status, data)
  return data as T
}

export type Field = { key: string; title: string; type: string; options?: string[]; ask?: string; required?: boolean }

export type Spec = {
  company: string
  script: string
  voice: string
  variables?: Record<string, string>
  vocabulary?: string[]
  max_call_seconds: number
  form?: Field[]
}

export type Version = { id: string; n: number; author: string; note: string; accepted_at: string | null; created_at: string; spec: Spec }
export type Agent = { id: string; name: string; template: string; created_at: string; latest?: Version }
export type Problem = { field: string; text: string }
export type AgentDetail = { agent: Agent; latest: Version; versions: Version[]; problems: Problem[]; variables: string[]; prompt: string }

export type Rules = { from: string; to: string; days: number[]; max_attempts: number; retry_minutes: number; first_portion: number; portion: number }

export type Campaign = {
  id: string
  name: string
  version_id: string
  agent_id: string
  agent_name: string
  version_n: number
  version_accepted: boolean
  list_id: string | null
  list_name: string | null
  line: string
  rules: Rules
  status: string
  pause_reason: string
  portion: number
  created_at: string
  started_at: string | null
  finished_at: string | null
  counts: Record<string, number>
  crm: CrmRules | null
  crm_failed: number
}

export type CrmConnection = { id: string; kind: string; name: string; base: string; created_at: string }
export type Pipeline = { entity: string; id: string; name: string; stages: { id: string; name: string }[] }
export type StageRef = { pipeline_id: string; stage_id: string }
export type CrmRules = {
  connection_id: string
  note: boolean
  move?: Record<string, StageRef>
  task?: Record<string, string>
  feed?: { entity: string; pipeline_id: string; stage_id: string } | null
}

export type Answer = { value: unknown; quote?: string; edited?: boolean }
export type Turn = { role: string; text?: string; name?: string; at_ms?: number; interrupted?: boolean }

export type Call = {
  id: string
  kind: string
  campaign_id: string | null
  version_id: string
  agent_name: string
  version_n: number
  phone: string
  status: string
  end_reason: string
  outcome: string
  duration_seconds: number | null
  charged_g: number | null
  checks: Record<string, boolean>
  analysis: string
  accepted: boolean | null
  feedback: string
  summary: string
  created_at: string
  started_at: string | null
  ended_at: string | null
  settled: boolean
  has_recording: boolean
  transcript?: Turn[] | null
  answers?: Record<string, Answer>
  form?: Field[]
}

export type Me = {
  workspace: { id: string; name: string; tester_phone: string; channels: number }
  voices?: { id: string; name: string; gender: string; available: boolean }[]
  lines?: { id: string; number: string; registration?: string }[]
  g_per_minute?: number
  online?: boolean
  open_calls: number
  needs: string[] | null
  gapi_error?: string
}

export type ContactList = {
  id: string
  name: string
  columns: { key: string; title: string }[]
  stats: { rows: number; accepted: number; duplicates: number; invalid: number; stopped: number; phone_column: string }
  created_at: string
}

export type Target = {
  id: string
  row: number
  phone: string
  vars: Record<string, string>
  portion: number
  status: string
  attempts: number
  next_at: string | null
  result: string
  call_id: string | null
  duration_seconds: number | null
  summary: string | null
  answers: Record<string, Answer> | null
  ref?: string
}
