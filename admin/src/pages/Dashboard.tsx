import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, errMsg, fmtTime } from '../api'
import { AuditEvent, Overview, ServiceEntry } from '../types'
import { DevSeedTag, ErrorBanner, HealthBadge, PageHeader, SkeletonRows, StatCard } from '../components'

export default function Dashboard() {
  const { t } = useTranslation('pages')
  const [ov, setOv] = useState<Overview | null>(null)
  const [services, setServices] = useState<ServiceEntry[]>([])
  const [events, setEvents] = useState<AuditEvent[]>([])
  const [auditSource, setAuditSource] = useState('')
  const [svcLoaded, setSvcLoaded] = useState(false)
  const [evLoaded, setEvLoaded] = useState(false)
  const [ovErr, setOvErr] = useState('')
  const [svcErr, setSvcErr] = useState('')
  const [evErr, setEvErr] = useState('')

  function load() {
    setOvErr('')
    setSvcErr('')
    setEvErr('')
    api.get('/v1/admin/overview').then((r) => setOv(r.data)).catch((e) => setOvErr(errMsg(e)))
    api.get('/v1/admin/services').then((r) => setServices(r.data.services || [])).catch((e) => setSvcErr(errMsg(e))).finally(() => setSvcLoaded(true))
    api.get('/v1/admin/audit/events').then((r) => {
      setEvents((r.data.events || []).slice(0, 8))
      setAuditSource(r.data.source)
    }).catch((e) => setEvErr(errMsg(e))).finally(() => setEvLoaded(true))
  }
  useEffect(load, [])

  const gateOpen = ov ? Object.values(ov.gates).filter(Boolean).length : 0
  const gateTotal = ov ? Object.keys(ov.gates).length : 0

  return (
    <div>
      <PageHeader title={t('dashboard.title')} sub={t('dashboard.sub')} />
      {ovErr && <ErrorBanner message={ovErr} onRetry={load} />}
      <div className="grid grid-cols-2 md:grid-cols-3 xl:grid-cols-6 gap-4 mb-8">
        <StatCard label={t('dashboard.servicesHealthy')} value={ov ? `${ov.services.healthy}/${ov.services.total}` : '—'} sub={t('dashboard.registeredServices')} />
        <StatCard label={t('dashboard.rulePacks')} value={ov?.packs.count ?? '—'} seed={ov?.packs.source === 'dev-seed'} />
        <StatCard label={t('dashboard.tenants')} value={ov?.tenants.count ?? '—'} />
        <StatCard label={t('dashboard.ledgerTransfers')} value={ov?.transfers.count ?? '—'} seed={ov?.transfers.source === 'dev-seed'} />
        <StatCard label={t('dashboard.evidenceObjects')} value={ov?.evidence_objects.count ?? '—'} seed={ov?.evidence_objects.source === 'dev-seed'} />
        <StatCard label={t('dashboard.gatesOpen')} value={`${gateOpen}/${gateTotal}`} sub={t('dashboard.regulatoryGates')} />
      </div>

      <div className="grid lg:grid-cols-2 gap-6">
        <section className="card p-5">
          <h2 className="text-sm font-semibold text-stone-900 mb-4">{t('dashboard.serviceRollup')}</h2>
          <div className="space-y-2 max-h-96 overflow-y-auto pr-1">
            {services.filter((s) => s.kind === 'service').map((s) => (
              <div key={s.id} className="flex items-center justify-between gap-3 text-sm">
                <div className="min-w-0">
                  <span className="font-medium text-stone-800">{s.name}</span>
                  <span className="ml-2 text-xs text-stone-600">{s.plane}</span>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  {s.latency_ms !== undefined && s.health_status === 'ok' && (
                    <span className="text-xs text-stone-600">{s.latency_ms}ms</span>
                  )}
                  <HealthBadge status={s.health_status} />
                </div>
              </div>
            ))}
            {svcErr && <ErrorBanner message={svcErr} onRetry={load} />}
            {!svcLoaded && <SkeletonRows rows={6} height="h-6" />}
            {svcLoaded && services.length === 0 && <div className="text-sm text-stone-600">{t('dashboard.noServices')}</div>}
          </div>
          <p className="mt-3 text-xs text-stone-600">
            {t('dashboard.unreachableNote')}
          </p>
        </section>

        <section className="card p-5">
          <div className="flex items-center justify-between mb-4">
            <h2 className="text-sm font-semibold text-stone-900">{t('dashboard.recentEvents')}</h2>
            <DevSeedTag source={auditSource} />
          </div>
          <div className="space-y-3 max-h-96 overflow-y-auto pr-1">
            {events.map((e) => (
              <div key={e.id} className="text-sm border-l-2 border-neutral-200 pl-3">
                <div className="flex items-baseline justify-between gap-2">
                  <span className="font-mono text-xs text-brand-700">{e.type}</span>
                  <span className="text-xs text-stone-600 shrink-0">{fmtTime(e.timestamp)}</span>
                </div>
                <div className="text-stone-700 mt-0.5">{e.detail || e.action}</div>
                <div className="text-xs text-stone-600 mt-0.5">{e.actor} · {e.subject}</div>
              </div>
            ))}
            {evErr && <ErrorBanner message={evErr} onRetry={load} />}
            {!evLoaded && <SkeletonRows rows={5} height="h-10" />}
            {evLoaded && events.length === 0 && <div className="text-sm text-stone-600">{t('dashboard.noEvents')}</div>}
          </div>
        </section>
      </div>
    </div>
  )
}
