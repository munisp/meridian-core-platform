import { useEffect, useState } from 'react'
import { api, fmtTime } from '../api'
import { Gate } from '../types'
import { Badge, DevSeedTag, Modal, PageHeader } from '../components'

interface GazetteRow { instrument: string; status: string; gate: string; checked_at: string }

export default function Gates() {
  const [gates, setGates] = useState<Gate[]>([])
  const [source, setSource] = useState('')
  const [gazette, setGazette] = useState<GazetteRow[]>([])
  const [target, setTarget] = useState<Gate | null>(null)
  const [reason, setReason] = useState('')
  const [err, setErr] = useState('')

  function load() {
    api.get('/v1/admin/gates').then((r) => {
      setGates(r.data.gates || [])
      setSource(r.data.source)
    }).catch(() => {})
    api.get('/v1/admin/gazette-watch').then((r) => setGazette(r.data.watch || [])).catch(() => {})
  }
  useEffect(load, [])

  async function flip() {
    if (!target) return
    setErr('')
    try {
      await api.post(`/v1/admin/gates/${target.id}/flip`, { confirm: true, reason })
      setTarget(null)
      setReason('')
      load()
    } catch (ex: any) {
      setErr(ex.response?.data?.detail || 'Flip failed (requires board role)')
    }
  }

  return (
    <div>
      <PageHeader
        title="Gates & Reg-watch"
        sub="Regulatory gates are board-authorised switches. Flips are armed, confirmed and audited. Gazette watch tracks the instruments behind each gate."
        actions={<DevSeedTag source={source} />}
      />
      <div className="card overflow-x-auto mb-8">
        <table className="w-full">
          <thead>
            <tr><th className="th">Gate</th><th className="th">Description</th><th className="th">State</th><th className="th">Updated</th><th className="th"></th></tr>
          </thead>
          <tbody>
            {gates.map((g) => (
              <tr key={g.id} className="hover:bg-sand-50">
                <td className="td">
                  <div className="font-mono text-xs font-semibold text-sand-900">{g.id}</div>
                  <div className="text-xs text-sand-500">{g.name}</div>
                </td>
                <td className="td text-xs max-w-md">{g.description}</td>
                <td className="td">
                  <Badge tone={g.state ? 'green' : 'red'}>{g.state ? 'OPEN' : 'CLOSED'}</Badge>
                  {g.armed_by && <div className="text-xs text-sand-400 mt-1">by {g.armed_by}</div>}
                </td>
                <td className="td text-xs">{fmtTime(g.updated_at)}</td>
                <td className="td">
                  <button className={g.state ? 'btn-danger text-xs' : 'btn-primary text-xs'} onClick={() => setTarget(g)}>
                    {g.state ? 'Close gate' : 'Open gate'}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <section>
        <h2 className="text-sm font-semibold text-sand-900 mb-3">Gazette watch</h2>
        <div className="grid md:grid-cols-2 gap-4">
          {gazette.map((g) => (
            <div key={g.instrument} className="card p-4">
              <div className="flex items-start justify-between gap-2">
                <div className="text-sm font-medium text-sand-800">{g.instrument}</div>
                <Badge tone="clay">{g.gate}</Badge>
              </div>
              <div className="mt-1.5 text-xs text-sand-600">{g.status}</div>
              <div className="mt-2 text-xs text-sand-400">checked {fmtTime(g.checked_at)}</div>
            </div>
          ))}
        </div>
      </section>

      <Modal open={!!target} title={`Confirm gate flip — ${target?.id}`} onClose={() => setTarget(null)}>
        <div className="space-y-4">
          <div className="rounded-lg bg-amber-50 border border-amber-200 px-4 py-3 text-sm text-amber-900">
            Gate flips are board-authorised and written to the append-only audit trail.
            You are about to {target?.state ? 'CLOSE' : 'OPEN'} <span className="font-mono font-semibold">{target?.id}</span>.
          </div>
          <div>
            <label className="label">Reason (recorded in audit trail)</label>
            <input className="input" value={reason} onChange={(e) => setReason(e.target.value)} placeholder="e.g. Gazette 2025-No-42 confirmed CTCs" />
          </div>
          {err && <div className="text-sm text-red-700">{err}</div>}
          <div className="flex justify-end gap-2">
            <button className="btn-secondary" onClick={() => setTarget(null)}>Cancel</button>
            <button className="btn-danger" disabled={!reason.trim()} onClick={flip}>Arm & confirm flip</button>
          </div>
        </div>
      </Modal>
    </div>
  )
}
