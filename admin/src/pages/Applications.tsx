import { useEffect, useState } from 'react'
import { api } from '../api'
import { ServiceEntry } from '../types'
import { Badge, HealthBadge, PageHeader } from '../components'

const PLANES = ['core', 'compliance', 'inclusion', 'gov'] as const
const PLANE_LABEL: Record<string, string> = {
  core: 'Core platform',
  compliance: 'Compliance suite (Market Zone)',
  inclusion: 'Inclusion suite (Market Zone)',
  gov: 'Gov enclave (Sovereign Zone)',
}

export default function Applications() {
  const [services, setServices] = useState<ServiceEntry[]>([])
  const [busy, setBusy] = useState('')

  function load() {
    api.get('/v1/admin/services').then((r) => setServices(r.data.services || [])).catch(() => {})
  }
  useEffect(load, [])

  async function toggle(id: string) {
    setBusy(id)
    try {
      await api.post(`/v1/admin/services/${id}/toggle`)
      load()
    } finally {
      setBusy('')
    }
  }

  return (
    <div>
      <PageHeader
        title="Applications"
        sub="Service registry: all 15 core services plus plane applications. Enable/disable controls health polling and route eligibility."
        actions={<button className="btn-secondary" onClick={load}>Refresh health</button>}
      />
      {PLANES.map((plane) => {
        const rows = services.filter((s) => s.plane === plane)
        if (rows.length === 0) return null
        return (
          <section key={plane} className="mb-8">
            <h2 className="text-xs font-semibold uppercase tracking-wide text-sand-500 mb-3">{PLANE_LABEL[plane]}</h2>
            <div className="grid md:grid-cols-2 xl:grid-cols-3 gap-4">
              {rows.map((s) => (
                <div key={s.id} className="card p-5 flex flex-col">
                  <div className="flex items-start justify-between gap-2">
                    <div>
                      <div className="font-semibold text-sand-900">{s.name}</div>
                      <div className="text-xs text-sand-400 font-mono mt-0.5">{s.id} · v{s.version}</div>
                    </div>
                    <HealthBadge status={s.health_status} />
                  </div>
                  {s.description && <p className="mt-2 text-sm text-sand-600 flex-1">{s.description}</p>}
                  <div className="mt-3 flex flex-wrap gap-1.5">
                    {s.t_items.map((t) => <Badge key={t} tone="sand">{t}</Badge>)}
                    <Badge tone="clay">{s.kind}</Badge>
                  </div>
                  <div className="mt-4 flex items-center justify-between border-t border-sand-100 pt-3">
                    <a href={s.base_url} target="_blank" rel="noreferrer" className="text-xs text-clay-700 hover:text-clay-900 font-mono truncate max-w-[60%]">
                      {s.base_url}
                    </a>
                    <button
                      className={s.enabled ? 'btn-secondary text-xs' : 'btn-primary text-xs'}
                      disabled={busy === s.id || s.id === 'admin-api'}
                      onClick={() => toggle(s.id)}
                    >
                      {s.enabled ? 'Disable' : 'Enable'}
                    </button>
                  </div>
                </div>
              ))}
            </div>
          </section>
        )
      })}
    </div>
  )
}
