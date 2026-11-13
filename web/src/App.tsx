import { createContext, useCallback, useContext, useEffect, useState } from 'react'
import { NavLink, Route, Routes } from 'react-router-dom'
import { api, ApiError, type Me } from './api'
import { say } from './ru'
import Home from './pages/Home'
import Agents from './pages/Agents'
import AgentPage from './pages/Agent'
import Lists from './pages/Lists'
import Campaigns from './pages/Campaigns'
import CampaignPage from './pages/Campaign'
import CallPage from './pages/Call'
import StopList from './pages/StopList'
import Crm from './pages/Crm'

type Session = { me: Me; reload: () => Promise<void> }
const Ctx = createContext<Session | null>(null)
export const useSession = () => useContext(Ctx)!

// usePoll reloads on an interval while the page is open: calls move on by
// themselves, and the page should follow without a refresh.
export function usePoll(load: () => void, ms: number, on = true) {
  useEffect(() => {
    if (!on) return
    const id = setInterval(load, ms)
    return () => clearInterval(id)
  }, [load, ms, on])
}

function Connect({ onDone }: { onDone: () => void }) {
  const [token, setToken] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await api('POST', '/connect', { token: token.trim() })
      onDone()
    } catch (err) {
      setError(say(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="connect">
      <div className="brand">
        <img src="/favicon.svg" alt="" />
        <div>
          Gapi<span>Voice Agent Studio</span>
        </div>
      </div>
      <form className="card" onSubmit={submit}>
        <h2>Подключите проект Gapi</h2>
        <p className="muted small">
          Studio звонит через ваш проект Gapi: его линию, голоса и баланс. Вставьте токен проекта из кабинета Gapi, страница «Токены». Токен хранится
          в зашифрованном виде.
        </p>
        <label>
          Токен проекта
          <input value={token} onChange={(e) => setToken(e.target.value)} placeholder="gapi_…" autoComplete="off" type="password" />
        </label>
        {error && <div className="note bad">{error}</div>}
        <button className="primary" disabled={busy || token.trim().length < 20}>
          Подключить
        </button>
      </form>
    </div>
  )
}

export default function App() {
  const [me, setMe] = useState<Me | null>(null)
  const [state, setState] = useState<'loading' | 'out' | 'in' | 'error'>('loading')
  const reload = useCallback(async () => {
    try {
      setMe(await api<Me>('GET', '/me'))
      setState('in')
    } catch (e) {
      setState(e instanceof ApiError && e.status === 401 ? 'out' : 'error')
    }
  }, [])
  useEffect(() => {
    void reload()
  }, [reload])

  if (state === 'loading') return null
  if (state === 'error') return <div className="connect note bad">Сервис Studio не отвечает. Обновите страницу.</div>
  if (state === 'out' || !me) return <Connect onDone={reload} />

  async function logout() {
    await api('POST', '/logout')
    setState('out')
  }
  return (
    <Ctx.Provider value={{ me, reload }}>
      <div className="shell">
        <nav className="rail">
          <div className="brand">
            <img src="/favicon.svg" alt="" />
            <div>
              Gapi<span>Voice Agent Studio</span>
            </div>
          </div>
          <NavLink to="/" end>
            Обзор
          </NavLink>
          <NavLink to="/agents">Агенты</NavLink>
          <NavLink to="/lists">Контакты</NavLink>
          <NavLink to="/campaigns">Обзвоны</NavLink>
          <NavLink to="/crm">CRM</NavLink>
          <NavLink to="/stoplist">Стоп-лист</NavLink>
          <div className="foot">
            {me.workspace.name || 'Проект Gapi'}
            <br />
            Каналы: <span className="num">{me.open_calls}</span> из <span className="num">{me.workspace.channels}</span>
            <br />
            <button className="quiet" onClick={logout}>
              Выйти
            </button>
          </div>
        </nav>
        <main>
          <Routes>
            <Route path="/" element={<Home />} />
            <Route path="/agents" element={<Agents />} />
            <Route path="/agents/:id" element={<AgentPage />} />
            <Route path="/lists" element={<Lists />} />
            <Route path="/campaigns" element={<Campaigns />} />
            <Route path="/campaigns/:id" element={<CampaignPage />} />
            <Route path="/calls/:id" element={<CallPage />} />
            <Route path="/crm" element={<Crm />} />
            <Route path="/stoplist" element={<StopList />} />
          </Routes>
        </main>
      </div>
    </Ctx.Provider>
  )
}
