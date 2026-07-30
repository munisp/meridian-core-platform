import { FormEvent, useEffect, useState } from 'react'
import { api, fmtTime } from '../api'
import { Badge, DevSeedTag, PageHeader } from '../components'

interface APIKey { id: string; name: string; prefix: string; scopes: string; created_at: string; revoked: boolean; secret_tail: string }
interface NotifProvider { channel: string; provider: string; mode: string; status: string }
interface RouteRow { plane: string; path: string; upstream: string; methods: string; auth: string }

export default function Settings() {
  const [flags, setFlags] = useState<Record<string, boolean>>({})
  const [keys, setKeys] = useState<APIKey[]>([])
  const [providers, setProviders] = useState<NotifProvider[]>([])
  const [routes, setRoutes] = useState<RouteRow[]>([])
  const [routeSource, setRouteSource] = useState('')
  const [waf, setWaf] = useState('detect')
  const [keyName, setKeyName] = useState('')
  const [secretOnce, setSecretOnce] = useState('')
  const [msg, setMsg] = useState('')

  function load() {
    api.get('/v1/admin/settings/flags').then((r) => setFlags(r.data.flags || {})).catch(() => {})
    api.get('/v1/admin/settings/api-keys').then((r) => setKeys(r.data.api_keys || [])).catch(() => {})
    api.get('/v1/admin/settings/notifications').then((r) => setProviders(r.data.providers || [])).catch(() => {})
    api.get('/v1/admin/settings/routes').then((r) => {
      setRoutes(r.data.routes || [])
      setRouteSource(r.data.source)
      if (r.data.waf_mode) setWaf(r.data.waf_mode)
    }).catch(() => {})
  }
  useEffect(load, [])

  async function toggleFlag(k: string) {
    const next = { ...flags, [k]: !flags[k] }
    setFlags(next)
    await api.put('/v1/admin/settings/flags', { flags: { [k]: next[k] } }).catch(() => {})
  }

  async function setWAF(mode: string) {
    setWaf(mode)
    try {
      await api.post('/v1/admin/settings/waf-mode', { mode })
      setMsg(`WAF mode → ${mode}`)
    } catch { setMsg('WAF mode change failed (requires admin role)') }
  }

  async function createKey(e: FormEvent) {
    e.preventDefault()
    try {
      const { data } = await api.post('/v1/admin/settings/api-keys', { name: keyName, scopes: 'read:packs read:health' })
      setSecretOnce(data.secret_once)
      setKeyName('')
      load()
    } catch (ex: any) { setMsg(ex.response?.data?.detail || 'Key create failed') }
  }

  async function revokeKey(id: string) {
    await api.post(`/v1/admin/settings/api-keys/${id}/revoke`).catch(() => {})
    load()
  }

  return (
    <div>
      <PageHeader title="Settings" sub="Edge policy & WAF mode, notification providers, feature flags and API keys." />
      {msg && <div className="mb-4 rounded-lg bg-moss-50 border border-moss-200 px-4 py-2.5 text-sm text-moss-800">{msg}</div>}

      <div className="grid lg:grid-cols-2 gap-6">
        <section className="card p-5">
          <div className="flex items-center justify-between mb-4">
            <h2 className="text-sm font-semibold text-sand-900">Edge policy — route table (APISIX)</h2>
            <DevSeedTag source={routeSource} />
          </div>
          <table className="w-full mb-5">
            <thead><tr><th className="th">Plane</th><th className="th">Path</th><th className="th">Upstream</th><th className="th">Auth</th></tr></thead>
            <tbody>
              {routes.map((r, i) => (
                <tr key={i}>
                  <td className="td text-xs">{r.plane}</td>
                  <td className="td font-mono text-xs">{r.path}</td>
                  <td className="td font-mono text-xs">{r.upstream}</td>
                  <td className="td"><Badge tone="sand">{r.auth}</Badge></td>
                </tr>
              ))}
            </tbody>
          </table>
          <h3 className="text-xs font-semibold uppercase tracking-wide text-sand-500 mb-2">WAF mode</h3>
          <div className="flex gap-2">
            {['detect', 'enforce'].map((m) => (
              <button key={m} onClick={() => setWAF(m)}
                className={waf === m ? 'btn-primary text-xs' : 'btn-secondary text-xs'}>
                {m}
              </button>
            ))}
            <span className="self-center text-xs text-sand-500">current: <span className="font-mono">{waf}</span></span>
          </div>
        </section>

        <section className="card p-5">
          <h2 className="text-sm font-semibold text-sand-900 mb-4">Notification providers</h2>
          <table className="w-full">
            <thead><tr><th className="th">Channel</th><th className="th">Provider</th><th className="th">Mode</th><th className="th">Status</th></tr></thead>
            <tbody>
              {providers.map((p) => (
                <tr key={p.channel}>
                  <td className="td font-medium text-xs uppercase">{p.channel}</td>
                  <td className="td text-xs">{p.provider}</td>
                  <td className="td"><Badge tone={p.mode === 'simulator' ? 'amber' : 'green'}>{p.mode}</Badge></td>
                  <td className="td"><Badge tone={p.status === 'ok' ? 'green' : 'amber'}>{p.status}</Badge></td>
                </tr>
              ))}
            </tbody>
          </table>
          <p className="mt-3 text-xs text-sand-400">All providers run behind interfaces with log simulators in dev (honesty tag: no real SMS/email is sent).</p>
        </section>

        <section className="card p-5">
          <h2 className="text-sm font-semibold text-sand-900 mb-4">Feature flags</h2>
          <div className="space-y-2.5">
            {Object.entries(flags).map(([k, v]) => (
              <div key={k} className="flex items-center justify-between">
                <span className="font-mono text-xs text-sand-700">{k}</span>
                <button
                  onClick={() => toggleFlag(k)}
                  className={`relative h-6 w-11 rounded-full transition-colors ${v ? 'bg-moss-500' : 'bg-sand-300'}`}
                  aria-label={`toggle ${k}`}
                >
                  <span className={`absolute top-0.5 h-5 w-5 rounded-full bg-white shadow transition-all ${v ? 'left-[22px]' : 'left-0.5'}`} />
                </button>
              </div>
            ))}
          </div>
        </section>

        <section className="card p-5">
          <h2 className="text-sm font-semibold text-sand-900 mb-4">API keys</h2>
          <form onSubmit={createKey} className="mb-4 flex gap-2">
            <input className="input" placeholder="key name e.g. ci ceremony bot" value={keyName} onChange={(e) => setKeyName(e.target.value)} required />
            <button className="btn-primary shrink-0">Create</button>
          </form>
          {secretOnce && (
            <div className="mb-4 rounded-lg bg-amber-50 border border-amber-200 px-3 py-2 text-xs font-mono text-amber-900 break-all">
              {secretOnce} — shown once, store it now.
            </div>
          )}
          <table className="w-full">
            <thead><tr><th className="th">Name</th><th className="th">Prefix</th><th className="th">Scopes</th><th className="th">Created</th><th className="th"></th></tr></thead>
            <tbody>
              {keys.map((k) => (
                <tr key={k.id} className={k.revoked ? 'opacity-50' : ''}>
                  <td className="td text-xs">{k.name}</td>
                  <td className="td font-mono text-xs">{k.prefix}{k.secret_tail}</td>
                  <td className="td font-mono text-xs">{k.scopes}</td>
                  <td className="td text-xs">{fmtTime(k.created_at)}</td>
                  <td className="td">
                    {k.revoked ? <Badge tone="red">revoked</Badge> : (
                      <button className="btn-secondary text-xs" onClick={() => revokeKey(k.id)}>Revoke</button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      </div>
    </div>
  )
}
