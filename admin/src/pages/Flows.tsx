import { useEffect, useState } from 'react'
import { api, fmtTime } from '../api'
import { FlowDef, FlowReceipt } from '../types'
import { Badge, DevSeedTag, PageHeader } from '../components'

export default function Flows() {
  const [flows, setFlows] = useState<FlowDef[]>([])
  const [receipts, setReceipts] = useState<FlowReceipt[]>([])
  const [rcptSource, setRcptSource] = useState('')
  const [forbidden, setForbidden] = useState<{ status: string; sightings: FlowReceipt[] } | null>(null)

  useEffect(() => {
    api.get('/v1/admin/flows/matrix').then((r) => setFlows(r.data.flows || [])).catch(() => {})
    api.get('/v1/admin/flows/receipts').then((r) => {
      setReceipts(r.data.receipts || [])
      setRcptSource(r.data.source)
    }).catch(() => {})
    api.get('/v1/admin/flows/forbidden').then((r) => setForbidden(r.data)).catch(() => {})
  }, [])

  return (
    <div>
      <PageHeader
        title="Cross-Zone Flows"
        sub="F1–F10 matrix from the unified architecture. All cross-zone traffic passes the audited enclave-gateway with synchronous WORM receipts; F9/F10 are forbidden by construction."
      />

      <section className={`mb-8 rounded-xl border p-5 ${forbidden?.status === 'clean' ? 'bg-moss-50 border-moss-200' : 'bg-red-50 border-red-300'}`}>
        <div className="flex items-center justify-between">
          <div>
            <div className="text-sm font-semibold text-sand-900">Forbidden-flow monitor (F9 / F10)</div>
            <div className="text-xs text-sand-600 mt-0.5">
              {forbidden?.status === 'clean'
                ? 'Clean — no sightings. This monitor must always be empty; any sighting is a security incident.'
                : `${forbidden?.sightings.length ?? 0} SIGHTING(S) — SECURITY INCIDENT`}
            </div>
          </div>
          <Badge tone={forbidden?.status === 'clean' ? 'green' : 'red'}>{forbidden?.status ?? '…'}</Badge>
        </div>
      </section>

      <section className="mb-8">
        <h2 className="text-sm font-semibold text-sand-900 mb-3">Flow matrix F1–F10</h2>
        <div className="card overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr><th className="th">Flow</th><th className="th">Direction</th><th className="th">Payload</th><th className="th">Topics</th><th className="th">Policy</th><th className="th">Note</th></tr>
            </thead>
            <tbody>
              {flows.map((f) => (
                <tr key={f.id} className={f.allowed ? 'hover:bg-sand-50' : 'bg-red-50/40'}>
                  <td className="td">
                    <div className="font-mono text-sm font-semibold text-sand-900">{f.id}</div>
                    <div className="text-xs text-sand-600">{f.name}</div>
                  </td>
                  <td className="td text-xs whitespace-nowrap">{f.direction}</td>
                  <td className="td text-xs max-w-[220px]">{f.payload}</td>
                  <td className="td font-mono text-xs">{f.topics}</td>
                  <td className="td"><Badge tone={f.allowed ? 'green' : 'red'}>{f.allowed ? 'allowed' : 'FORBIDDEN'}</Badge></td>
                  <td className="td text-xs max-w-[280px]">{f.note}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section>
        <div className="flex items-center justify-between mb-3">
          <h2 className="text-sm font-semibold text-sand-900">Receipt log (audited cross-zone messages)</h2>
          <DevSeedTag source={rcptSource} />
        </div>
        <div className="card overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr><th className="th">Receipt</th><th className="th">Flow</th><th className="th">Sender</th><th className="th">WORM URI</th><th className="th">sha256</th><th className="th">Status</th><th className="th">Time</th></tr>
            </thead>
            <tbody>
              {receipts.map((r) => (
                <tr key={r.id} className="hover:bg-sand-50">
                  <td className="td font-mono text-xs">{r.id}<div className="text-sand-400">{r.correlation_id}</div></td>
                  <td className="td"><Badge tone="clay">{r.flow}</Badge></td>
                  <td className="td text-xs">{r.sender}</td>
                  <td className="td font-mono text-xs max-w-[240px] truncate">{r.worm_uri}</td>
                  <td className="td font-mono text-xs max-w-[140px] truncate">{r.sha256}</td>
                  <td className="td"><Badge tone={r.status === 'accepted' ? 'green' : 'red'}>{r.status}</Badge></td>
                  <td className="td text-xs whitespace-nowrap">{fmtTime(r.timestamp)}</td>
                </tr>
              ))}
              {receipts.length === 0 && <tr><td className="td text-center text-sand-400" colSpan={7}>No receipts recorded.</td></tr>}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  )
}
