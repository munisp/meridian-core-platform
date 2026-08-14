import { FormEvent, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, errMsg, fmtKobo, fmtTime } from '../api'
import { LedgerAccount, ReconBreak } from '../types'
import { Badge, DevSeedTag, ErrorBanner, Modal, PageHeader } from '../components'
import Field from '../components/Field'
import MoneyInput from '../components/MoneyInput'

const LEDGER_NAMES: Record<number, string> = {
  100: 'agent_float', 200: 'psm_payments', 300: 'vat_remittance', 400: 'pssp_recon',
  500: 'dispute_deposits', 600: 't11_attribution', 700: 'commissions',
}

export default function Ledger() {
  const { t } = useTranslation('pages')
  const [accounts, setAccounts] = useState<LedgerAccount[]>([])
  const [source, setSource] = useState('')
  const [breaks, setBreaks] = useState<ReconBreak[]>([])
  const [lookup, setLookup] = useState('')
  const [lookupResult, setLookupResult] = useState('')
  const [showTransfer, setShowTransfer] = useState(false)
  const [form, setForm] = useState({ debit_account_id: '', credit_account_id: '', ledger: 200 })
  const [amountKobo, setAmountKobo] = useState<number | null>(null)
  const [msg, setMsg] = useState('')
  const [loadErr, setLoadErr] = useState('')

  function load() {
    setLoadErr('')
    const errs: string[] = []
    api.get('/v1/admin/ledger/accounts').then((r) => {
      setAccounts(r.data.accounts || [])
      setSource(r.data.source)
    }).catch((e) => { errs.push(errMsg(e)); setLoadErr(errs.join(' · ')) })
    api.get('/v1/admin/ledger/recon-breaks').then((r) => setBreaks(r.data.breaks || []))
      .catch((e) => { errs.push(errMsg(e)); setLoadErr(errs.join(' · ')) })
  }
  useEffect(load, [])

  async function doLookup(e: FormEvent) {
    e.preventDefault()
    try {
      const { data } = await api.get(`/v1/admin/ledger/accounts/${encodeURIComponent(lookup)}/balance`)
      setLookupResult(`${data.account_id}: ${fmtKobo(data.balance_kobo)} ${data.currency} (${data.source})`)
    } catch {
      setLookupResult(t('ledger.accountNotFound'))
    }
  }

  async function submitTransfer(e: FormEvent) {
    e.preventDefault()
    setMsg('')
    try {
      const { data } = await api.post('/v1/admin/ledger/transfers', {
        debit_account_id: form.debit_account_id,
        credit_account_id: form.credit_account_id,
        amount_kobo: amountKobo,
        ledger: Number(form.ledger),
      })
      setMsg(t('ledger.pendingCreated', { id: data.id }))
      setShowTransfer(false)
      load()
    } catch (ex: any) {
      setMsg(ex.response?.data?.detail || t('ledger.transferFailed'))
    }
  }

  return (
    <div>
      <PageHeader
        title={t('ledger.title')}
        sub={t('ledger.sub')}
        actions={
          <>
            <DevSeedTag source={source} />
            <button className="btn-primary" onClick={() => setShowTransfer(true)}>{t('ledger.newTransfer')}</button>
          </>
        }
      />
      {loadErr && <ErrorBanner message={loadErr} onRetry={load} />}
      {msg && <div role="status" className="mb-4 rounded-lg bg-success border border-brand-200 px-4 py-2.5 text-sm text-success-on">{msg}</div>}

      <form onSubmit={doLookup} className="mb-6 flex gap-2 max-w-xl">
        <label htmlFor="acct-lookup" className="sr-only">{t('ledger.accountIdLabel')}</label>
        <input id="acct-lookup" className="input font-mono text-xs" placeholder={t('ledger.lookupPlaceholder')} value={lookup} onChange={(e) => setLookup(e.target.value)} />
        <button className="btn-secondary shrink-0">{t('ledger.balanceLookup')}</button>
      </form>
      {lookupResult && <div className="mb-6 font-mono text-sm text-stone-800">{lookupResult}</div>}

      <div className="card overflow-x-auto mb-8">
        <table className="w-full">
          <thead>
            <tr><th scope="col" className="th">{t('ledger.th.account')}</th><th scope="col" className="th">{t('ledger.th.ledger')}</th><th scope="col" className="th">{t('ledger.th.owner')}</th><th scope="col" className="th">{t('ledger.th.flags')}</th><th className="th text-right">{t('ledger.th.balance')}</th></tr>
          </thead>
          <tbody>
            {accounts.map((a) => (
              <tr key={a.id} className="hover:bg-neutral-50">
                <td className="td font-mono text-xs">{a.id}</td>
                <td className="td text-xs">{a.ledger} · {LEDGER_NAMES[a.ledger] || ''}</td>
                <td className="td text-xs">{a.owner}</td>
                <td className="td">{a.flags ? <Badge tone="amber">{a.flags}</Badge> : <span className="text-xs text-stone-600">—</span>}</td>
                <td className="td text-right font-mono text-sm">{fmtKobo(a.balance_kobo)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <section>
        <h2 className="text-sm font-semibold text-stone-900 mb-3">{t('ledger.reconTitle')}</h2>
        <div className="card overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr><th scope="col" className="th">{t('ledger.reconTh.break')}</th><th scope="col" className="th">{t('ledger.reconTh.kind')}</th><th className="th text-right">{t('ledger.reconTh.expected')}</th><th className="th text-right">{t('ledger.reconTh.actual')}</th><th scope="col" className="th">{t('ledger.reconTh.detail')}</th><th scope="col" className="th">{t('ledger.reconTh.status')}</th></tr>
            </thead>
            <tbody>
              {breaks.map((b) => (
                <tr key={b.id} className="hover:bg-neutral-50">
                  <td className="td font-mono text-xs">{b.id}<div className="text-stone-600">{fmtTime(b.opened_at)}</div></td>
                  <td className="td text-xs">{b.kind}</td>
                  <td className="td text-right font-mono text-xs">{fmtKobo(b.expected_kobo)}</td>
                  <td className="td text-right font-mono text-xs">{fmtKobo(b.actual_kobo)}</td>
                  <td className="td text-xs max-w-sm">{b.detail}</td>
                  <td className="td"><Badge tone={b.status === 'open' ? 'red' : 'green'}>{b.status}</Badge></td>
                </tr>
              ))}
              {breaks.length === 0 && <tr><td className="td text-center text-stone-600" colSpan={6}>{t('ledger.noBreaks')}</td></tr>}
            </tbody>
          </table>
        </div>
      </section>

      <Modal open={showTransfer} title={t('ledger.modalTitle')} onClose={() => setShowTransfer(false)}>
        <form onSubmit={submitTransfer} className="space-y-4">
          <Field label={t('ledger.debitAccount')} required>
            {(id) => (
              <input id={id} className="input font-mono text-xs" required value={form.debit_account_id} onChange={(e) => setForm({ ...form, debit_account_id: e.target.value })} placeholder="100|4|op-float-lagos-01" />
            )}
          </Field>
          <Field label={t('ledger.creditAccount')} required>
            {(id) => (
              <input id={id} className="input font-mono text-xs" required value={form.credit_account_id} onChange={(e) => setForm({ ...form, credit_account_id: e.target.value })} placeholder="200|5|psm-settlement-pssp" />
            )}
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('ledger.amount')} required>
              {(id, describedBy, invalid) => (
                <MoneyInput id={id} valueKobo={amountKobo} onChangeKobo={setAmountKobo} invalid={invalid} aria-describedby={describedBy} aria-required={true} />
              )}
            </Field>
            <Field label={t('ledger.ledgerLabel')}>
              {(id) => (
                <select id={id} className="input" value={form.ledger} onChange={(e) => setForm({ ...form, ledger: Number(e.target.value) })}>
                  {Object.entries(LEDGER_NAMES).map(([k, v]) => <option key={k} value={k}>{k} · {v}</option>)}
                </select>
              )}
            </Field>
          </div>
          <p className="text-xs text-stone-600">{t('ledger.transferNote')}</p>
          <div className="flex justify-end gap-2">
            <button type="button" className="btn-secondary" onClick={() => setShowTransfer(false)}>{t('ledger.cancel')}</button>
            <button className="btn-primary" disabled={amountKobo == null || amountKobo <= 0}>{t('ledger.createPending')}</button>
          </div>
        </form>
      </Modal>
    </div>
  )
}
