import { FormEvent, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, errMsg, fmtTime, sha256Hex } from '../api'
import { AuditEvent, EvidenceObject } from '../types'
import { Badge, DevSeedTag, ErrorBanner, Modal, PageHeader } from '../components'

export default function Audit() {
  const { t } = useTranslation('pages')
  const [events, setEvents] = useState<AuditEvent[]>([])
  const [evSource, setEvSource] = useState('')
  const [evidence, setEvidence] = useState<EvidenceObject[]>([])
  const [subject, setSubject] = useState('')
  const [type, setType] = useState('')
  const [selected, setSelected] = useState<EvidenceObject | null>(null)
  const [verify, setVerify] = useState<'idle' | 'ok' | 'fail'>('idle')
  const [tatSubject, setTatSubject] = useState('')
  const [tat, setTat] = useState<AuditEvent[] | null>(null)
  const [evErr, setEvErr] = useState('')
  const [docsErr, setDocsErr] = useState('')

  function loadEvents() {
    const q = new URLSearchParams()
    if (subject) q.set('subject', subject)
    if (type) q.set('type', type)
    api.get('/v1/admin/audit/events?' + q.toString()).then((r) => {
      setEvents(r.data.events || [])
      setEvSource(r.data.source)
    }).catch((e) => setEvErr(errMsg(e)))
  }
  function loadEvidence() {
    setDocsErr('')
    api.get('/v1/admin/evidence').then((r) => setEvidence(r.data.evidence || [])).catch((e) => setDocsErr(errMsg(e)))
  }
  useEffect(() => { loadEvents(); loadEvidence() }, [])

  async function openEvidence(id: string) {
    setVerify('idle')
    try {
      const { data } = await api.get(`/v1/admin/evidence/${id}`)
      setSelected(data.evidence || data)
    } catch {
      setSelected(evidence.find((e) => e.id === id) || null)
    }
  }

  async function verifyHash() {
    if (!selected) return
    const hex = await sha256Hex(selected.content)
    setVerify(hex === selected.sha256 ? 'ok' : 'fail')
  }

  async function assembleTAT(e: FormEvent) {
    e.preventDefault()
    const { data } = await api.post('/v1/admin/tat/assemble', { subject: tatSubject })
    setTat(data.entries || [])
  }

  return (
    <div>
      <PageHeader
        title={t('audit.title')}
        sub={t('audit.sub')}
        actions={<DevSeedTag source={evSource} />}
      />
      {evErr && <ErrorBanner message={evErr} onRetry={loadEvents} />}
      {docsErr && <ErrorBanner message={docsErr} onRetry={loadEvidence} />}

      <div className="grid lg:grid-cols-5 gap-6">
        <section className="lg:col-span-3">
          <form className="mb-4 flex flex-wrap gap-2" onSubmit={(e) => { e.preventDefault(); loadEvents() }}>
            <label htmlFor="audit-subject" className="sr-only">{t('audit.subjectFilter')}</label>
            <input id="audit-subject" className="input max-w-[220px]" placeholder={t('audit.subjectPlaceholder')} value={subject} onChange={(e) => setSubject(e.target.value)} />
            <label htmlFor="audit-type" className="sr-only">{t('audit.typeFilter')}</label>
            <input id="audit-type" className="input max-w-[200px]" placeholder={t('audit.typePlaceholder')} value={type} onChange={(e) => setType(e.target.value)} />
            <button className="btn-secondary">{t('audit.search')}</button>
          </form>
          <div className="card overflow-x-auto max-h-[560px] overflow-y-auto">
            <table className="w-full">
              <thead className="sticky top-0 bg-white">
                <tr><th scope="col" className="th">{t('audit.th.type')}</th><th scope="col" className="th">{t('audit.th.subject')}</th><th scope="col" className="th">{t('audit.th.actor')}</th><th scope="col" className="th">{t('audit.th.detail')}</th><th scope="col" className="th">{t('audit.th.time')}</th></tr>
              </thead>
              <tbody>
                {events.map((e) => (
                  <tr key={e.id} className="hover:bg-neutral-50">
                    <td className="td font-mono text-xs text-brand-700">{e.type}</td>
                    <td className="td font-mono text-xs">{e.subject}</td>
                    <td className="td text-xs">{e.actor}</td>
                    <td className="td text-xs max-w-[220px]">{e.detail || e.action}</td>
                    <td className="td text-xs whitespace-nowrap">{fmtTime(e.timestamp)}</td>
                  </tr>
                ))}
                {events.length === 0 && <tr><td className="td text-center text-stone-600" colSpan={5}>{t('audit.noEvents')}</td></tr>}
              </tbody>
            </table>
          </div>
        </section>

        <section className="lg:col-span-2">
          <h2 className="text-sm font-semibold text-stone-900 mb-3">{t('audit.wormTitle')}</h2>
          <div className="space-y-3 mb-8">
            {evidence.map((e) => (
              <button key={e.id} className="card w-full p-4 text-left hover:border-brand-300 transition-colors" onClick={() => openEvidence(e.id)}>
                <div className="flex items-center justify-between gap-2">
                  <span className="font-mono text-xs text-stone-800">{e.id}</span>
                  <Badge tone="clay">{e.kind}</Badge>
                </div>
                <div className="mt-1.5 font-mono text-xs text-stone-600 truncate">sha256 {e.sha256}</div>
                <div className="mt-1 text-xs text-stone-600">{e.worm_uri} · {fmtTime(e.created_at)}</div>
              </button>
            ))}
            {evidence.length === 0 && <div className="text-sm text-stone-600">{t('audit.noEvidence')}</div>}
          </div>

          <h2 className="text-sm font-semibold text-stone-900 mb-3">{t('audit.tatTitle')}</h2>
          <form onSubmit={assembleTAT} className="flex gap-2">
            <label htmlFor="tat-subject" className="sr-only">{t('audit.tatSubject')}</label>
            <input id="tat-subject" className="input" placeholder={t('audit.tatPlaceholder')} value={tatSubject} onChange={(e) => setTatSubject(e.target.value)} />
            <button className="btn-secondary shrink-0">{t('audit.assemble')}</button>
          </form>
          {tat && (
            <div className="card mt-3 p-4 max-h-56 overflow-y-auto">
              <div className="text-xs text-stone-600 mb-2">{t('audit.tatSummary', { count: tat.length })}</div>
              {tat.map((t) => (
                <div key={t.id} className="text-xs py-1 border-b border-neutral-100 last:border-0">
                  <span className="font-mono text-brand-700">{t.type}</span> · {t.actor} · {fmtTime(t.timestamp)}
                  {t.rule_pack_version && <span className="text-stone-600"> · {t.rule_pack_version}</span>}
                </div>
              ))}
              {tat.length === 0 && <div className="text-xs text-stone-600">{t('audit.noTatEntries')}</div>}
            </div>
          )}
        </section>
      </div>

      <Modal open={!!selected} title={selected?.id || ''} onClose={() => setSelected(null)}>
        {selected && (
          <div className="space-y-3">
            <div className="flex flex-wrap gap-2">
              <Badge tone="clay">{selected.kind}</Badge>
              <Badge tone={selected.immutable ? 'green' : 'amber'}>{selected.immutable ? t('audit.immutable') : t('audit.mutable')}</Badge>
            </div>
            <div className="text-xs text-stone-600 font-mono break-all">{selected.worm_uri}</div>
            <pre className="max-h-64 overflow-auto rounded-lg bg-neutral-50 border border-neutral-200 p-4 text-xs font-mono whitespace-pre-wrap">{selected.content}</pre>
            <div className="font-mono text-xs text-stone-600 break-all">sha256: {selected.sha256}</div>
            <div className="flex items-center gap-3">
              <button className="btn-primary text-xs" onClick={verifyHash}>{t('audit.verify')}</button>
              {verify === 'ok' && <Badge tone="green">{t('audit.hashOk')}</Badge>}
              {verify === 'fail' && <Badge tone="red">{t('audit.hashFail')}</Badge>}
            </div>
          </div>
        )}
      </Modal>
    </div>
  )
}
