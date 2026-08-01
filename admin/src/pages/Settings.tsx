import { FormEvent, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, fmtTime } from '../api'
import { Badge, DevSeedTag, PageHeader } from '../components'

interface APIKey { id: string; name: string; prefix: string; scopes: string; created_at: string; revoked: boolean; secret_tail: string }
interface NotifProvider { channel: string; provider: string; mode: string; status: string }
interface RouteRow { plane: string; path: string; upstream: string; methods: string; auth: string }

export default function Settings() {
  const { t } = useTranslation('pages')
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
      setMsg(t('settings.wafChanged', { mode }))
    } catch { setMsg(t('settings.wafFailed')) }
  }

  async function createKey(e: FormEvent) {
    e.preventDefault()
    try {
      const { data } = await api.post('/v1/admin/settings/api-keys', { name: keyName, scopes: 'read:packs read:health' })
      setSecretOnce(data.secret_once)
      setKeyName('')
      load()
    } catch (ex: any) { setMsg(ex.response?.data?.detail || t('settings.keyCreateFailed')) }
  }

  async function revokeKey(id: string) {
    await api.post(`/v1/admin/settings/api-keys/${id}/revoke`).catch(() => {})
    load()
  }

  return (
    <div>
      <PageHeader title={t('settings.title')} sub={t('settings.sub')} />
      {msg && <div role="status" className="mb-4 rounded-lg bg-success border border-brand-200 px-4 py-2.5 text-sm text-success-on">{msg}</div>}

      <div className="grid lg:grid-cols-2 gap-6">
        <section className="card p-5">
          <div className="flex items-center justify-between mb-4">
            <h2 className="text-sm font-semibold text-stone-900">{t('settings.edgeTitle')}</h2>
            <DevSeedTag source={routeSource} />
          </div>
          <table className="w-full mb-5">
            <thead><tr><th scope="col" className="th">{t('settings.edgeTh.plane')}</th><th scope="col" className="th">{t('settings.edgeTh.path')}</th><th scope="col" className="th">{t('settings.edgeTh.upstream')}</th><th scope="col" className="th">{t('settings.edgeTh.auth')}</th></tr></thead>
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
          <h3 className="text-xs font-semibold uppercase tracking-wide text-stone-600 mb-2">{t('settings.wafTitle')}</h3>
          <div className="flex gap-2">
            {['detect', 'enforce'].map((m) => (
              <button key={m} onClick={() => setWAF(m)}
                className={waf === m ? 'btn-primary text-xs' : 'btn-secondary text-xs'}>
                {m}
              </button>
            ))}
            <span className="self-center text-xs text-stone-600">{t('settings.current')} <span className="font-mono">{waf}</span></span>
          </div>
        </section>

        <section className="card p-5">
          <h2 className="text-sm font-semibold text-stone-900 mb-4">{t('settings.notifTitle')}</h2>
          <table className="w-full">
            <thead><tr><th scope="col" className="th">{t('settings.notifTh.channel')}</th><th scope="col" className="th">{t('settings.notifTh.provider')}</th><th scope="col" className="th">{t('settings.notifTh.mode')}</th><th scope="col" className="th">{t('settings.notifTh.status')}</th></tr></thead>
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
          <p className="mt-3 text-xs text-stone-600">{t('settings.notifNote')}</p>
        </section>

        <section className="card p-5">
          <h2 className="text-sm font-semibold text-stone-900 mb-4">{t('settings.flagsTitle')}</h2>
          <div className="space-y-2.5">
            {Object.entries(flags).map(([k, v]) => (
              <div key={k} className="flex items-center justify-between">
                <span className="font-mono text-xs text-stone-700">{k}</span>
                <button
                  role="switch"
                  aria-checked={v}
                  onClick={() => toggleFlag(k)}
                  className={`relative h-6 w-11 rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-700 focus-visible:ring-offset-2 ${v ? 'bg-brand-700' : 'bg-neutral-300'}`}
                  aria-label={t('settings.toggleLabel', { key: k })}
                >
                  <span className={`absolute top-0.5 h-5 w-5 rounded-full bg-white shadow transition-all ${v ? 'left-[22px]' : 'left-0.5'}`} />
                </button>
              </div>
            ))}
          </div>
        </section>

        <section className="card p-5">
          <h2 className="text-sm font-semibold text-stone-900 mb-4">{t('settings.keysTitle')}</h2>
          <form onSubmit={createKey} className="mb-4 flex gap-2">
            <label htmlFor="api-key-name" className="sr-only">{t('settings.keyNameLabel')}</label>
            <input id="api-key-name" className="input" placeholder={t('settings.keyPlaceholder')} value={keyName} onChange={(e) => setKeyName(e.target.value)} required />
            <button className="btn-primary shrink-0">{t('settings.create')}</button>
          </form>
          {secretOnce && (
            <div className="mb-4 rounded-lg bg-warning border border-warning-strong px-3 py-2 text-xs font-mono text-warning-on break-all">
              {secretOnce} {t('settings.secretNote')}
            </div>
          )}
          <table className="w-full">
            <thead><tr><th scope="col" className="th">{t('settings.keysTh.name')}</th><th scope="col" className="th">{t('settings.keysTh.prefix')}</th><th scope="col" className="th">{t('settings.keysTh.scopes')}</th><th scope="col" className="th">{t('settings.keysTh.created')}</th><th scope="col" className="th"></th></tr></thead>
            <tbody>
              {keys.map((k) => (
                <tr key={k.id} className={k.revoked ? 'opacity-50' : ''}>
                  <td className="td text-xs">{k.name}</td>
                  <td className="td font-mono text-xs">{k.prefix}{k.secret_tail}</td>
                  <td className="td font-mono text-xs">{k.scopes}</td>
                  <td className="td text-xs">{fmtTime(k.created_at)}</td>
                  <td className="td">
                    {k.revoked ? <Badge tone="red">{t('settings.revoked')}</Badge> : (
                      <button className="btn-secondary text-xs" onClick={() => revokeKey(k.id)}>{t('settings.revoke')}</button>
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
