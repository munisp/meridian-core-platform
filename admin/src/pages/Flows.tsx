import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, errMsg, fmtTime } from '../api'
import { FlowDef, FlowReceipt } from '../types'
import { Badge, DevSeedTag, ErrorBanner, PageHeader } from '../components'

export default function Flows() {
  const { t } = useTranslation('pages')
  const [flows, setFlows] = useState<FlowDef[]>([])
  const [receipts, setReceipts] = useState<FlowReceipt[]>([])
  const [rcptSource, setRcptSource] = useState('')
  const [forbidden, setForbidden] = useState<{ status: string; sightings: FlowReceipt[] } | null>(null)
  const [loadErr, setLoadErr] = useState('')

  function load() {
    setLoadErr('')
    const errs: string[] = []
    api.get('/v1/admin/flows/matrix').then((r) => setFlows(r.data.flows || []))
      .catch((e) => { errs.push(errMsg(e)); setLoadErr(errs.join(' · ')) })
    api.get('/v1/admin/flows/receipts').then((r) => {
      setReceipts(r.data.receipts || [])
      setRcptSource(r.data.source)
    }).catch((e) => { errs.push(errMsg(e)); setLoadErr(errs.join(' · ')) })
    api.get('/v1/admin/flows/forbidden').then((r) => setForbidden(r.data))
      .catch((e) => { errs.push(errMsg(e)); setLoadErr(errs.join(' · ')) })
  }
  useEffect(load, [])

  return (
    <div>
      <PageHeader
        title={t('flows.title')}
        sub={t('flows.sub')}
      />
      {loadErr && <ErrorBanner message={loadErr} onRetry={load} />}

      <section className={`mb-8 rounded-xl border p-5 ${forbidden?.status === 'clean' ? 'bg-success border-brand-200' : 'bg-danger border-danger-strong'}`}>
        <div className="flex items-center justify-between">
          <div>
            <div className="text-sm font-semibold text-stone-900">{t('flows.forbiddenTitle')}</div>
            <div className="text-xs text-stone-600 mt-0.5">
              {forbidden?.status === 'clean'
                ? t('flows.forbiddenClean')
                : t('flows.forbiddenSightings', { count: forbidden?.sightings.length ?? 0 })}
            </div>
          </div>
          <Badge tone={forbidden?.status === 'clean' ? 'green' : 'red'}>{forbidden?.status ?? '…'}</Badge>
        </div>
      </section>

      <section className="mb-8">
        <h2 className="text-sm font-semibold text-stone-900 mb-3">{t('flows.matrixTitle')}</h2>
        <div className="card overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr><th scope="col" className="th">{t('flows.th.flow')}</th><th scope="col" className="th">{t('flows.th.direction')}</th><th scope="col" className="th">{t('flows.th.payload')}</th><th scope="col" className="th">{t('flows.th.topics')}</th><th scope="col" className="th">{t('flows.th.policy')}</th><th scope="col" className="th">{t('flows.th.note')}</th></tr>
            </thead>
            <tbody>
              {flows.map((f) => (
                <tr key={f.id} className={f.allowed ? 'hover:bg-neutral-50' : 'bg-danger/60'}>
                  <td className="td">
                    <div className="font-mono text-sm font-semibold text-stone-900">{f.id}</div>
                    <div className="text-xs text-stone-600">{f.name}</div>
                  </td>
                  <td className="td text-xs whitespace-nowrap">{f.direction}</td>
                  <td className="td text-xs max-w-[220px]">{f.payload}</td>
                  <td className="td font-mono text-xs">{f.topics}</td>
                  <td className="td"><Badge tone={f.allowed ? 'green' : 'red'}>{f.allowed ? t('flows.allowed') : t('flows.forbidden')}</Badge></td>
                  <td className="td text-xs max-w-[280px]">{f.note}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section>
        <div className="flex items-center justify-between mb-3">
          <h2 className="text-sm font-semibold text-stone-900">{t('flows.receiptTitle')}</h2>
          <DevSeedTag source={rcptSource} />
        </div>
        <div className="card overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr><th scope="col" className="th">{t('flows.receiptTh.receipt')}</th><th scope="col" className="th">{t('flows.receiptTh.flow')}</th><th scope="col" className="th">{t('flows.receiptTh.sender')}</th><th scope="col" className="th">{t('flows.receiptTh.wormUri')}</th><th scope="col" className="th">{t('flows.receiptTh.sha256')}</th><th scope="col" className="th">{t('flows.receiptTh.status')}</th><th scope="col" className="th">{t('flows.receiptTh.time')}</th></tr>
            </thead>
            <tbody>
              {receipts.map((r) => (
                <tr key={r.id} className="hover:bg-neutral-50">
                  <td className="td font-mono text-xs">{r.id}<div className="text-stone-600">{r.correlation_id}</div></td>
                  <td className="td"><Badge tone="clay">{r.flow}</Badge></td>
                  <td className="td text-xs">{r.sender}</td>
                  <td className="td font-mono text-xs max-w-[240px] truncate">{r.worm_uri}</td>
                  <td className="td font-mono text-xs max-w-[140px] truncate">{r.sha256}</td>
                  <td className="td"><Badge tone={r.status === 'accepted' ? 'green' : 'red'}>{r.status}</Badge></td>
                  <td className="td text-xs whitespace-nowrap">{fmtTime(r.timestamp)}</td>
                </tr>
              ))}
              {receipts.length === 0 && <tr><td className="td text-center text-stone-600" colSpan={7}>{t('flows.noReceipts')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  )
}
