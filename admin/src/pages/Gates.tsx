import { useEffect, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { api, errMsg, fmtTime } from '../api'
import { Gate } from '../types'
import { Badge, DevSeedTag, ErrorBanner, Modal, PageHeader } from '../components'
import Field from '../components/Field'

interface GazetteRow { instrument: string; status: string; gate: string; checked_at: string }

export default function Gates() {
  const { t } = useTranslation('pages')
  const [gates, setGates] = useState<Gate[]>([])
  const [source, setSource] = useState('')
  const [gazette, setGazette] = useState<GazetteRow[]>([])
  const [target, setTarget] = useState<Gate | null>(null)
  const [reason, setReason] = useState('')
  const [err, setErr] = useState('')
  const [loadErr, setLoadErr] = useState('')

  function load() {
    setLoadErr('')
    const errs: string[] = []
    api.get('/v1/admin/gates').then((r) => {
      setGates(r.data.gates || [])
      setSource(r.data.source)
    }).catch((e) => { errs.push(errMsg(e)); setLoadErr(errs.join(' · ')) })
    api.get('/v1/admin/gazette-watch').then((r) => setGazette(r.data.watch || []))
      .catch((e) => { errs.push(errMsg(e)); setLoadErr(errs.join(' · ')) })
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
      setErr(ex.response?.data?.detail || t('gates.flipFailed'))
    }
  }

  return (
    <div>
      <PageHeader
        title={t('gates.title')}
        sub={t('gates.sub')}
        actions={<DevSeedTag source={source} />}
      />
      {loadErr && <ErrorBanner message={loadErr} onRetry={load} />}
      <div className="card overflow-x-auto mb-8">
        <table className="w-full">
          <thead>
            <tr><th scope="col" className="th">{t('gates.th.gate')}</th><th scope="col" className="th">{t('gates.th.description')}</th><th scope="col" className="th">{t('gates.th.state')}</th><th scope="col" className="th">{t('gates.th.updated')}</th><th scope="col" className="th"></th></tr>
          </thead>
          <tbody>
            {gates.map((g) => (
              <tr key={g.id} className="hover:bg-neutral-50">
                <td className="td">
                  <div className="font-mono text-xs font-semibold text-stone-900">{g.id}</div>
                  <div className="text-xs text-stone-600">{g.name}</div>
                </td>
                <td className="td text-xs max-w-md">{g.description}</td>
                <td className="td">
                  <Badge tone={g.state ? 'green' : 'red'}>{g.state ? t('gates.open') : t('gates.closed')}</Badge>
                  {g.armed_by && <div className="text-xs text-stone-600 mt-1">{t('gates.armedBy', { actor: g.armed_by })}</div>}
                </td>
                <td className="td text-xs">{fmtTime(g.updated_at)}</td>
                <td className="td">
                  <button className={g.state ? 'btn-danger text-xs' : 'btn-primary text-xs'} onClick={() => setTarget(g)}>
                    {g.state ? t('gates.closeGate') : t('gates.openGate')}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <section>
        <h2 className="text-sm font-semibold text-stone-900 mb-3">{t('gates.gazetteWatch')}</h2>
        <div className="grid md:grid-cols-2 gap-4">
          {gazette.map((g) => (
            <div key={g.instrument} className="card p-4">
              <div className="flex items-start justify-between gap-2">
                <div className="text-sm font-medium text-stone-800">{g.instrument}</div>
                <Badge tone="clay">{g.gate}</Badge>
              </div>
              <div className="mt-1.5 text-xs text-stone-600">{g.status}</div>
              <div className="mt-2 text-xs text-stone-600">{t('gates.checked', { time: fmtTime(g.checked_at) })}</div>
            </div>
          ))}
        </div>
      </section>

      <Modal open={!!target} title={t('gates.modalTitle', { id: target?.id })} onClose={() => setTarget(null)}>
        <div className="space-y-4">
          <div className="rounded-lg bg-warning border border-warning-strong px-4 py-3 text-sm text-warning-on">
            <Trans
              i18nKey="gates.flipWarn"
              ns="pages"
              values={{ action: target?.state ? t('gates.closed') : t('gates.open'), id: target?.id }}
              components={{ 1: <span className="font-mono font-semibold" /> }}
            />
          </div>
          <Field label={t('gates.reasonLabel')} required>
            {(id) => (
              <input id={id} className="input" value={reason} onChange={(e) => setReason(e.target.value)} placeholder={t('gates.reasonPlaceholder')} aria-required="true" />
            )}
          </Field>
          {err && <div role="alert" className="text-sm text-danger-strong">{err}</div>}
          <div className="flex justify-end gap-2">
            <button className="btn-secondary" onClick={() => setTarget(null)}>{t('gates.cancel')}</button>
            <button className="btn-danger" disabled={!reason.trim()} onClick={flip}>{t('gates.armConfirm')}</button>
          </div>
        </div>
      </Modal>
    </div>
  )
}
