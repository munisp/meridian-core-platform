import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../api'
import { PackSummary } from '../types'
import { Badge, DevSeedTag, Modal, PageHeader } from '../components'

interface PackDetail {
  summary: PackSummary
  yaml: string
  signature: { algorithm: string; key_id: string; verified: boolean }
  source?: string
  [k: string]: unknown
}

export default function RulePacks() {
  const { t } = useTranslation('pages')
  const [packs, setPacks] = useState<PackSummary[]>([])
  const [source, setSource] = useState('')
  const [stale, setStale] = useState<string[]>([])
  const [detail, setDetail] = useState<PackDetail | null>(null)
  const [msg, setMsg] = useState('')

  function load() {
    api.get('/v1/admin/packs').then((r) => {
      setPacks(r.data.packs || [])
      setSource(r.data.source)
      setStale(r.data.stale_consumers || [])
    }).catch(() => {})
  }
  useEffect(load, [])

  async function open(id: string) {
    const { data } = await api.get(`/v1/admin/packs/${id}`)
    setDetail(data)
  }

  async function publish(id: string, ver: string) {
    if (!confirm(t('rulePacks.publishConfirm', { id, ver }))) return
    try {
      const { data } = await api.post(`/v1/admin/packs/${id}/${ver}/publish`)
      setMsg(t('rulePacks.publishedMsg', { id, ver, source: data.source || 'live' }))
      load()
    } catch (ex: any) {
      setMsg(ex.response?.data?.detail || t('rulePacks.publishFailed'))
    }
  }

  return (
    <div>
      <PageHeader
        title={t('rulePacks.title')}
        sub={t('rulePacks.sub')}
        actions={<DevSeedTag source={source} />}
      />
      {msg && <div role="status" className="mb-4 rounded-lg bg-success border border-brand-200 px-4 py-2.5 text-sm text-success-on">{msg}</div>}
      {stale.length > 0 && (
        <div role="alert" className="mb-4 rounded-lg bg-warning border border-warning-strong px-4 py-2.5 text-sm text-warning-on">
          {t('rulePacks.staleConsumers', { list: stale.join(', ') })}
        </div>
      )}
      <div className="card overflow-x-auto">
        <table className="w-full">
          <thead>
            <tr>
              <th scope="col" className="th">{t('rulePacks.th.pack')}</th><th scope="col" className="th">{t('rulePacks.th.version')}</th><th scope="col" className="th">{t('rulePacks.th.status')}</th>
              <th scope="col" className="th">{t('rulePacks.th.effective')}</th><th scope="col" className="th">{t('rulePacks.th.signed')}</th><th scope="col" className="th">{t('rulePacks.th.citation')}</th><th scope="col" className="th"></th>
            </tr>
          </thead>
          <tbody>
            {packs.map((p) => (
              <tr key={p.id} className="hover:bg-neutral-50">
                <td className="td font-mono text-xs">
                  {p.id}
                  {p.subject_to_regazette && <div className="mt-1"><Badge tone="amber">{t('rulePacks.subjectToRegazette')}</Badge></div>}
                </td>
                <td className="td font-mono text-xs">{p.latest_version}</td>
                <td className="td">
                  <Badge tone={p.status === 'published' ? 'green' : p.status === 'retired' ? 'sand' : 'amber'}>{p.status}</Badge>
                  {p.stale_consumers > 0 && <span className="ml-1"><Badge tone="red">{t('rulePacks.staleBadge', { count: p.stale_consumers })}</Badge></span>}
                </td>
                <td className="td text-xs">{p.effective_from}</td>
                <td className="td">{p.signed ? <Badge tone="green">{t('rulePacks.signedBadge')}</Badge> : <Badge tone="sand">{t('rulePacks.unsigned')}</Badge>}</td>
                <td className="td text-xs max-w-xs">{p.source_citation}</td>
                <td className="td whitespace-nowrap">
                  <button className="btn-secondary text-xs mr-1.5" onClick={() => open(p.id)}>{t('rulePacks.view')}</button>
                  {p.status !== 'published' && (
                    <button className="btn-primary text-xs" onClick={() => publish(p.id, p.latest_version)}>{t('rulePacks.publish')}</button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <Modal open={!!detail} title={detail ? `${detail.summary.id}@${detail.summary.latest_version}` : ''} onClose={() => setDetail(null)}>
        {detail && (
          <div className="space-y-3">
            <div className="flex flex-wrap gap-2">
              <Badge tone={detail.signature.verified ? 'green' : 'sand'}>
                {detail.signature.verified ? t('rulePacks.signedDetail', { algorithm: detail.signature.algorithm, keyId: detail.signature.key_id }) : t('rulePacks.unsigned')}
              </Badge>
              <Badge tone="clay">{detail.summary.status}</Badge>
              <DevSeedTag source={detail.source} />
            </div>
            <p className="text-sm text-stone-600">{detail.summary.source_citation}</p>
            <pre className="max-h-80 overflow-auto rounded-lg bg-neutral-50 border border-neutral-200 p-4 text-xs font-mono text-stone-800 whitespace-pre-wrap">
              {detail.yaml}
            </pre>
          </div>
        )}
      </Modal>
    </div>
  )
}
