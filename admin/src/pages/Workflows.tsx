import { FormEvent, useEffect, useState } from 'react'
import { api, fmtTime } from '../api'
import { WorkflowDef, WorkflowRun } from '../types'
import { Badge, Modal, PageHeader } from '../components'

export default function Workflows() {
  const [defs, setDefs] = useState<WorkflowDef[]>([])
  const [runs, setRuns] = useState<WorkflowRun[]>([])
  const [target, setTarget] = useState<WorkflowDef | null>(null)
  const [input, setInput] = useState('{}')
  const [msg, setMsg] = useState('')

  function load() {
    api.get('/v1/admin/workflows').then((r) => setDefs(r.data.workflows || [])).catch(() => {})
    api.get('/v1/admin/workflow-runs').then((r) => setRuns(r.data.runs || [])).catch(() => {})
  }
  useEffect(load, [])

  async function trigger(e: FormEvent) {
    e.preventDefault()
    if (!target) return
    try {
      JSON.parse(input || '{}')
    } catch {
      setMsg('Input must be valid JSON.')
      return
    }
    try {
      const { data } = await api.post(`/v1/admin/workflows/${target.id}/trigger`, { input })
      setMsg(`${target.id} triggered → run ${data.run.id} (${data.mode}, ${data.run.status})`)
      setTarget(null)
      load()
    } catch (ex: any) {
      setMsg(ex.response?.data?.detail || 'Trigger failed')
    }
  }

  const planes = [...new Set(defs.map((d) => d.plane))]

  return (
    <div>
      <PageHeader
        title="Workflows"
        sub="wf-* registry across planes. Triggers run via temporal-sdkx; with TEMPORAL_URL unset the dev in-process runner executes them and runs are audited."
      />
      {msg && <div className="mb-4 rounded-lg bg-moss-50 border border-moss-200 px-4 py-2.5 text-sm text-moss-800">{msg}</div>}

      {planes.map((plane) => (
        <section key={plane} className="mb-7">
          <h2 className="text-xs font-semibold uppercase tracking-wide text-sand-500 mb-3">{plane} plane</h2>
          <div className="grid md:grid-cols-2 xl:grid-cols-3 gap-4">
            {defs.filter((d) => d.plane === plane).map((d) => (
              <div key={d.id} className="card p-4">
                <div className="flex items-start justify-between gap-2">
                  <div>
                    <div className="font-medium text-sand-900 text-sm">{d.name}</div>
                    <div className="font-mono text-xs text-clay-700 mt-0.5">{d.id}</div>
                  </div>
                  {d.triggerable
                    ? <button className="btn-secondary text-xs shrink-0" onClick={() => { setTarget(d); setInput('{}') }}>Trigger</button>
                    : <Badge tone="sand">ceremony-only</Badge>}
                </div>
                <p className="mt-2 text-xs text-sand-600">{d.description}</p>
              </div>
            ))}
          </div>
        </section>
      ))}

      <section>
        <h2 className="text-sm font-semibold text-sand-900 mb-3">Run history (from audit)</h2>
        <div className="card overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr><th className="th">Run</th><th className="th">Workflow</th><th className="th">Triggered by</th><th className="th">Input</th><th className="th">Started</th><th className="th">Status</th></tr>
            </thead>
            <tbody>
              {runs.map((r) => (
                <tr key={r.id} className="hover:bg-sand-50">
                  <td className="td font-mono text-xs">{r.id}</td>
                  <td className="td font-mono text-xs">{r.workflow_id}</td>
                  <td className="td text-xs">{r.triggered_by}</td>
                  <td className="td font-mono text-xs max-w-[200px] truncate">{r.input || '—'}</td>
                  <td className="td text-xs">{fmtTime(r.started_at)}</td>
                  <td className="td">
                    <Badge tone={r.status === 'completed' ? 'green' : r.status === 'failed' ? 'red' : 'amber'}>{r.status}</Badge>
                  </td>
                </tr>
              ))}
              {runs.length === 0 && <tr><td className="td text-center text-sand-400" colSpan={6}>No runs recorded.</td></tr>}
            </tbody>
          </table>
        </div>
      </section>

      <Modal open={!!target} title={`Trigger ${target?.id}`} onClose={() => setTarget(null)}>
        <form onSubmit={trigger} className="space-y-4">
          <div>
            <label className="label">Input (JSON)</label>
            <textarea className="input font-mono text-xs h-28" value={input} onChange={(e) => setInput(e.target.value)} />
          </div>
          <div className="flex justify-end gap-2">
            <button type="button" className="btn-secondary" onClick={() => setTarget(null)}>Cancel</button>
            <button className="btn-primary">Trigger workflow</button>
          </div>
        </form>
      </Modal>
    </div>
  )
}
