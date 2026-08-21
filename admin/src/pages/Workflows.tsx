import { FormEvent, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, errMsg, fmtTime } from '../api'
import { WorkflowDef, WorkflowRun } from '../types'
import { Badge, ErrorBanner, Modal, PageHeader } from '../components'
import Field from '../components/Field'

export default function Workflows() {
  const { t } = useTranslation('pages')
  const [defs, setDefs] = useState<WorkflowDef[]>([])
  const [runs, setRuns] = useState<WorkflowRun[]>([])
  const [target, setTarget] = useState<WorkflowDef | null>(null)
  const [input, setInput] = useState('{}')
  const [msg, setMsg] = useState('')
  const [loadErr, setLoadErr] = useState('')

  function load() {
    setLoadErr('')
    const errs: string[] = []
    api.get('/v1/admin/workflows').then((r) => setDefs(r.data.workflows || []))
      .catch((e) => { errs.push(errMsg(e)); setLoadErr(errs.join(' · ')) })
    api.get('/v1/admin/workflow-runs').then((r) => setRuns(r.data.runs || []))
      .catch((e) => { errs.push(errMsg(e)); setLoadErr(errs.join(' · ')) })
  }
  useEffect(load, [])

  async function trigger(e: FormEvent) {
    e.preventDefault()
    if (!target) return
    try {
      JSON.parse(input || '{}')
    } catch {
      setMsg(t('workflows.invalidJson'))
      return
    }
    try {
      const { data } = await api.post(`/v1/admin/workflows/${target.id}/trigger`, { input })
      setMsg(t('workflows.triggeredMsg', { id: target.id, runId: data.run.id, mode: data.mode, status: data.run.status }))
      setTarget(null)
      load()
    } catch (ex: any) {
      setMsg(ex.response?.data?.detail || t('workflows.triggerFailed'))
    }
  }

  const planes = [...new Set(defs.map((d) => d.plane))]

  return (
    <div>
      <PageHeader
        title={t('workflows.title')}
        sub={t('workflows.sub')}
      />
      {msg && <div role="status" className="mb-4 rounded-lg bg-success border border-brand-200 px-4 py-2.5 text-sm text-success-on">{msg}</div>}
      {loadErr && <ErrorBanner message={loadErr} onRetry={load} />}

      {planes.map((plane) => (
        <section key={plane} className="mb-7">
          <h2 className="text-xs font-semibold uppercase tracking-wide text-stone-600 mb-3">{t('workflows.planeSuffix', { plane })}</h2>
          <div className="grid md:grid-cols-2 xl:grid-cols-3 gap-4">
            {defs.filter((d) => d.plane === plane).map((d) => (
              <div key={d.id} className="card p-4">
                <div className="flex items-start justify-between gap-2">
                  <div>
                    <div className="font-medium text-stone-900 text-sm">{d.name}</div>
                    <div className="font-mono text-xs text-brand-700 mt-0.5">{d.id}</div>
                  </div>
                  {d.triggerable
                    ? <button className="btn-secondary text-xs shrink-0" onClick={() => { setTarget(d); setInput('{}') }}>{t('workflows.trigger')}</button>
                    : <Badge tone="sand">{t('workflows.ceremonyOnly')}</Badge>}
                </div>
                <p className="mt-2 text-xs text-stone-600">{d.description}</p>
              </div>
            ))}
          </div>
        </section>
      ))}

      <section>
        <h2 className="text-sm font-semibold text-stone-900 mb-3">{t('workflows.runHistory')}</h2>
        <div className="card overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr><th scope="col" className="th">{t('workflows.th.run')}</th><th scope="col" className="th">{t('workflows.th.workflow')}</th><th scope="col" className="th">{t('workflows.th.triggeredBy')}</th><th scope="col" className="th">{t('workflows.th.input')}</th><th scope="col" className="th">{t('workflows.th.started')}</th><th scope="col" className="th">{t('workflows.th.status')}</th></tr>
            </thead>
            <tbody>
              {runs.map((r) => (
                <tr key={r.id} className="hover:bg-neutral-50">
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
              {runs.length === 0 && <tr><td className="td text-center text-stone-600" colSpan={6}>{t('workflows.noRuns')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>

      <Modal open={!!target} title={t('workflows.modalTitle', { id: target?.id })} onClose={() => setTarget(null)}>
        <form onSubmit={trigger} className="space-y-4">
          <Field label={t('workflows.inputLabel')} hint={t('workflows.inputHint')}>
            {(id, describedBy) => (
              <textarea id={id} aria-describedby={describedBy} className="input font-mono text-xs h-28 py-2" value={input} onChange={(e) => setInput(e.target.value)} />
            )}
          </Field>
          <div className="flex justify-end gap-2">
            <button type="button" className="btn-secondary" onClick={() => setTarget(null)}>{t('workflows.cancel')}</button>
            <button className="btn-primary">{t('workflows.triggerWorkflow')}</button>
          </div>
        </form>
      </Modal>
    </div>
  )
}
