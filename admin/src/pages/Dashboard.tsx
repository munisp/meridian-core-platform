import { useEffect, useState } from 'react'
import { api, fmtTime } from '../api'
import { AuditEvent, Overview, ServiceEntry } from '../types'
import { DevSeedTag, HealthBadge, PageHeader, StatCard } from '../components'

export default function Dashboard() {
  const [ov, setOv] = useState<Overview | null>(null)
  const [services, setServices] = useState<ServiceEntry[]>([])
  const [events, setEvents] = useState<AuditEvent[]>([])
  const [auditSource, setAuditSource] = useState('')

  useEffect(() => {
    api.get('/v1/admin/overview').then((r) => setOv(r.data)).catch(() => {})
    api.get('/v1/admin/services').then((r) => setServices(r.data.services || [])).catch(() => {})
    api.get('/v1/admin/audit/events').then((r) => {
      setEvents((r.data.events || []).slice(0, 8))
      setAuditSource(r.data.source)
    }).catch(() => {})
  }, [])

  const gateOpen = ov ? Object.values(ov.gates).filter(Boolean).length : 0
  const gateTotal = ov ? Object.keys(ov.gates).length : 0

  return (
    <div>
      <PageHeader title="Dashboard" sub="Platform health, control-plane counts and recent audited activity across all four planes." />
      <div className="grid grid-cols-2 md:grid-cols-3 xl:grid-cols-6 gap-4 mb-8">
        <StatCard label="Services healthy" value={ov ? `${ov.services.healthy}/${ov.services.total}` : '—'} sub="registered services" />
        <StatCard label="Rule packs" value={ov?.packs.count ?? '—'} seed={ov?.packs.source === 'dev-seed'} />
        <StatCard label="Tenants" value={ov?.tenants.count ?? '—'} />
        <StatCard label="Ledger transfers" value={ov?.transfers.count ?? '—'} seed={ov?.transfers.source === 'dev-seed'} />
        <StatCard label="Evidence objects" value={ov?.evidence_objects.count ?? '—'} seed={ov?.evidence_objects.source === 'dev-seed'} />
        <StatCard label="Gates open" value={`${gateOpen}/${gateTotal}`} sub="regulatory gates" />
      </div>

      <div className="grid lg:grid-cols-2 gap-6">
        <section className="card p-5">
          <h2 className="text-sm font-semibold text-sand-900 mb-4">Service health rollup</h2>
          <div className="space-y-2 max-h-96 overflow-y-auto pr-1">
            {services.filter((s) => s.kind === 'service').map((s) => (
              <div key={s.id} className="flex items-center justify-between gap-3 text-sm">
                <div className="min-w-0">
                  <span className="font-medium text-sand-800">{s.name}</span>
                  <span className="ml-2 text-xs text-sand-400">{s.plane}</span>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  {s.latency_ms !== undefined && s.health_status === 'ok' && (
                    <span className="text-xs text-sand-400">{s.latency_ms}ms</span>
                  )}
                  <HealthBadge status={s.health_status} />
                </div>
              </div>
            ))}
            {services.length === 0 && <div className="text-sm text-sand-400">Loading registry…</div>}
          </div>
          <p className="mt-3 text-xs text-sand-400">
            Unreachable = service not started in this dev environment; dependent views fall back to marked dev seeds.
          </p>
        </section>

        <section className="card p-5">
          <div className="flex items-center justify-between mb-4">
            <h2 className="text-sm font-semibold text-sand-900">Recent audit events</h2>
            <DevSeedTag source={auditSource} />
          </div>
          <div className="space-y-3 max-h-96 overflow-y-auto pr-1">
            {events.map((e) => (
              <div key={e.id} className="text-sm border-l-2 border-sand-200 pl-3">
                <div className="flex items-baseline justify-between gap-2">
                  <span className="font-mono text-xs text-clay-700">{e.type}</span>
                  <span className="text-xs text-sand-400 shrink-0">{fmtTime(e.timestamp)}</span>
                </div>
                <div className="text-sand-700 mt-0.5">{e.detail || e.action}</div>
                <div className="text-xs text-sand-400 mt-0.5">{e.actor} · {e.subject}</div>
              </div>
            ))}
            {events.length === 0 && <div className="text-sm text-sand-400">No events.</div>}
          </div>
        </section>
      </div>
    </div>
  )
}
