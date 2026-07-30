import { FormEvent, useEffect, useState } from 'react'
import { api, fmtTime, sha256Hex } from '../api'
import { AuditEvent, EvidenceObject } from '../types'
import { Badge, DevSeedTag, Modal, PageHeader } from '../components'

export default function Audit() {
  const [events, setEvents] = useState<AuditEvent[]>([])
  const [evSource, setEvSource] = useState('')
  const [evidence, setEvidence] = useState<EvidenceObject[]>([])
  const [subject, setSubject] = useState('')
  const [type, setType] = useState('')
  const [selected, setSelected] = useState<EvidenceObject | null>(null)
  const [verify, setVerify] = useState<'idle' | 'ok' | 'fail'>('idle')
  const [tatSubject, setTatSubject] = useState('')
  const [tat, setTat] = useState<AuditEvent[] | null>(null)

  function loadEvents() {
    const q = new URLSearchParams()
    if (subject) q.set('subject', subject)
    if (type) q.set('type', type)
    api.get('/v1/admin/audit/events?' + q.toString()).then((r) => {
      setEvents(r.data.events || [])
      setEvSource(r.data.source)
    }).catch(() => {})
  }
  function loadEvidence() {
    api.get('/v1/admin/evidence').then((r) => setEvidence(r.data.evidence || [])).catch(() => {})
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
        title="Audit & Evidence (WORM)"
        sub="Append-only audit trail, write-once-read-many evidence objects with in-browser sha256 verification (WebCrypto), and technical-audit-trail assembly."
        actions={<DevSeedTag source={evSource} />}
      />

      <div className="grid lg:grid-cols-5 gap-6">
        <section className="lg:col-span-3">
          <form className="mb-4 flex flex-wrap gap-2" onSubmit={(e) => { e.preventDefault(); loadEvents() }}>
            <input className="input max-w-[220px]" placeholder="subject contains…" value={subject} onChange={(e) => setSubject(e.target.value)} />
            <input className="input max-w-[200px]" placeholder="type prefix e.g. gate." value={type} onChange={(e) => setType(e.target.value)} />
            <button className="btn-secondary">Search</button>
          </form>
          <div className="card overflow-x-auto max-h-[560px] overflow-y-auto">
            <table className="w-full">
              <thead className="sticky top-0 bg-white">
                <tr><th className="th">Type</th><th className="th">Subject</th><th className="th">Actor</th><th className="th">Detail</th><th className="th">Time</th></tr>
              </thead>
              <tbody>
                {events.map((e) => (
                  <tr key={e.id} className="hover:bg-sand-50">
                    <td className="td font-mono text-xs text-clay-700">{e.type}</td>
                    <td className="td font-mono text-xs">{e.subject}</td>
                    <td className="td text-xs">{e.actor}</td>
                    <td className="td text-xs max-w-[220px]">{e.detail || e.action}</td>
                    <td className="td text-xs whitespace-nowrap">{fmtTime(e.timestamp)}</td>
                  </tr>
                ))}
                {events.length === 0 && <tr><td className="td text-center text-sand-400" colSpan={5}>No matching events.</td></tr>}
              </tbody>
            </table>
          </div>
        </section>

        <section className="lg:col-span-2">
          <h2 className="text-sm font-semibold text-sand-900 mb-3">WORM evidence objects</h2>
          <div className="space-y-3 mb-8">
            {evidence.map((e) => (
              <button key={e.id} className="card w-full p-4 text-left hover:border-clay-300 transition-colors" onClick={() => openEvidence(e.id)}>
                <div className="flex items-center justify-between gap-2">
                  <span className="font-mono text-xs text-sand-800">{e.id}</span>
                  <Badge tone="clay">{e.kind}</Badge>
                </div>
                <div className="mt-1.5 font-mono text-xs text-sand-400 truncate">sha256 {e.sha256}</div>
                <div className="mt-1 text-xs text-sand-500">{e.worm_uri} · {fmtTime(e.created_at)}</div>
              </button>
            ))}
            {evidence.length === 0 && <div className="text-sm text-sand-400">No evidence objects.</div>}
          </div>

          <h2 className="text-sm font-semibold text-sand-900 mb-3">TAT assembly</h2>
          <form onSubmit={assembleTAT} className="flex gap-2">
            <input className="input" placeholder="subject e.g. rp-wht-2024" value={tatSubject} onChange={(e) => setTatSubject(e.target.value)} />
            <button className="btn-secondary shrink-0">Assemble</button>
          </form>
          {tat && (
            <div className="card mt-3 p-4 max-h-56 overflow-y-auto">
              <div className="text-xs text-sand-500 mb-2">Who saw what, when, under which rule pack — {tat.length} entries</div>
              {tat.map((t) => (
                <div key={t.id} className="text-xs py-1 border-b border-sand-100 last:border-0">
                  <span className="font-mono text-clay-700">{t.type}</span> · {t.actor} · {fmtTime(t.timestamp)}
                  {t.rule_pack_version && <span className="text-sand-400"> · {t.rule_pack_version}</span>}
                </div>
              ))}
              {tat.length === 0 && <div className="text-xs text-sand-400">No entries for this subject.</div>}
            </div>
          )}
        </section>
      </div>

      <Modal open={!!selected} title={selected?.id || ''} onClose={() => setSelected(null)}>
        {selected && (
          <div className="space-y-3">
            <div className="flex flex-wrap gap-2">
              <Badge tone="clay">{selected.kind}</Badge>
              <Badge tone={selected.immutable ? 'green' : 'amber'}>{selected.immutable ? 'immutable (WORM)' : 'mutable'}</Badge>
            </div>
            <div className="text-xs text-sand-500 font-mono break-all">{selected.worm_uri}</div>
            <pre className="max-h-64 overflow-auto rounded-lg bg-sand-50 border border-sand-200 p-4 text-xs font-mono whitespace-pre-wrap">{selected.content}</pre>
            <div className="font-mono text-xs text-sand-600 break-all">sha256: {selected.sha256}</div>
            <div className="flex items-center gap-3">
              <button className="btn-primary text-xs" onClick={verifyHash}>Verify in browser (WebCrypto)</button>
              {verify === 'ok' && <Badge tone="green">hash matches — integrity confirmed</Badge>}
              {verify === 'fail' && <Badge tone="red">HASH MISMATCH — do not trust this object</Badge>}
            </div>
          </div>
        )}
      </Modal>
    </div>
  )
}
