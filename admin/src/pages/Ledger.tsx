import { FormEvent, useEffect, useState } from 'react'
import { api, fmtKobo, fmtTime } from '../api'
import { LedgerAccount, ReconBreak } from '../types'
import { Badge, DevSeedTag, Modal, PageHeader } from '../components'
import Field from '../components/Field'
import MoneyInput from '../components/MoneyInput'

const LEDGER_NAMES: Record<number, string> = {
  100: 'agent_float', 200: 'psm_payments', 300: 'vat_remittance', 400: 'pssp_recon',
  500: 'dispute_deposits', 600: 't11_attribution', 700: 'commissions',
}

export default function Ledger() {
  const [accounts, setAccounts] = useState<LedgerAccount[]>([])
  const [source, setSource] = useState('')
  const [breaks, setBreaks] = useState<ReconBreak[]>([])
  const [lookup, setLookup] = useState('')
  const [lookupResult, setLookupResult] = useState('')
  const [showTransfer, setShowTransfer] = useState(false)
  const [form, setForm] = useState({ debit_account_id: '', credit_account_id: '', ledger: 200 })
  const [amountKobo, setAmountKobo] = useState<number | null>(null)
  const [msg, setMsg] = useState('')

  function load() {
    api.get('/v1/admin/ledger/accounts').then((r) => {
      setAccounts(r.data.accounts || [])
      setSource(r.data.source)
    }).catch(() => {})
    api.get('/v1/admin/ledger/recon-breaks').then((r) => setBreaks(r.data.breaks || [])).catch(() => {})
  }
  useEffect(load, [])

  async function doLookup(e: FormEvent) {
    e.preventDefault()
    try {
      const { data } = await api.get(`/v1/admin/ledger/accounts/${encodeURIComponent(lookup)}/balance`)
      setLookupResult(`${data.account_id}: ${fmtKobo(data.balance_kobo)} ${data.currency} (${data.source})`)
    } catch {
      setLookupResult('Account not found.')
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
      setMsg(`Pending transfer ${data.id} created — post or void it from the ledger svc (code 1=authorise).`)
      setShowTransfer(false)
      load()
    } catch (ex: any) {
      setMsg(ex.response?.data?.detail || 'Transfer failed')
    }
  }

  return (
    <div>
      <PageHeader
        title="Ledger"
        sub="TigerBeetle double-entry scheme (integer kobo only). Accounts browser, balance lookup, transfer initiation and PSSP recon breaks."
        actions={
          <>
            <DevSeedTag source={source} />
            <button className="btn-primary" onClick={() => setShowTransfer(true)}>New transfer</button>
          </>
        }
      />
      {msg && <div role="status" className="mb-4 rounded-lg bg-success border border-brand-200 px-4 py-2.5 text-sm text-success-on">{msg}</div>}

      <form onSubmit={doLookup} className="mb-6 flex gap-2 max-w-xl">
        <label htmlFor="acct-lookup" className="sr-only">Account id</label>
        <input id="acct-lookup" className="input font-mono text-xs" placeholder="Account id e.g. 200|5|psm-settlement-pssp" value={lookup} onChange={(e) => setLookup(e.target.value)} />
        <button className="btn-secondary shrink-0">Balance lookup</button>
      </form>
      {lookupResult && <div className="mb-6 font-mono text-sm text-stone-800">{lookupResult}</div>}

      <div className="card overflow-x-auto mb-8">
        <table className="w-full">
          <thead>
            <tr><th scope="col" className="th">Account</th><th scope="col" className="th">Ledger</th><th scope="col" className="th">Owner</th><th scope="col" className="th">Flags</th><th className="th text-right">Balance</th></tr>
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
        <h2 className="text-sm font-semibold text-stone-900 mb-3">Recon breaks (PSSP 3-way)</h2>
        <div className="card overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr><th scope="col" className="th">Break</th><th scope="col" className="th">Kind</th><th className="th text-right">Expected</th><th className="th text-right">Actual</th><th scope="col" className="th">Detail</th><th scope="col" className="th">Status</th></tr>
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
              {breaks.length === 0 && <tr><td className="td text-center text-stone-600" colSpan={6}>No recon breaks.</td></tr>}
            </tbody>
          </table>
        </div>
      </section>

      <Modal open={showTransfer} title="Initiate transfer (pending / two-phase)" onClose={() => setShowTransfer(false)}>
        <form onSubmit={submitTransfer} className="space-y-4">
          <Field label="Debit account" required>
            {(id) => (
              <input id={id} className="input font-mono text-xs" required value={form.debit_account_id} onChange={(e) => setForm({ ...form, debit_account_id: e.target.value })} placeholder="100|4|op-float-lagos-01" />
            )}
          </Field>
          <Field label="Credit account" required>
            {(id) => (
              <input id={id} className="input font-mono text-xs" required value={form.credit_account_id} onChange={(e) => setForm({ ...form, credit_account_id: e.target.value })} placeholder="200|5|psm-settlement-pssp" />
            )}
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Amount (₦)" required>
              {(id, describedBy, invalid) => (
                <MoneyInput id={id} valueKobo={amountKobo} onChangeKobo={setAmountKobo} invalid={invalid} aria-describedby={describedBy} aria-required={true} />
              )}
            </Field>
            <Field label="Ledger">
              {(id) => (
                <select id={id} className="input" value={form.ledger} onChange={(e) => setForm({ ...form, ledger: Number(e.target.value) })}>
                  {Object.entries(LEDGER_NAMES).map(([k, v]) => <option key={k} value={k}>{k} · {v}</option>)}
                </select>
              )}
            </Field>
          </div>
          <p className="text-xs text-stone-600">Creates a pending (authorise) transfer. Post or void via the ledger service; float accounts enforce DEBITS_MUST_NOT_EXCEED_CREDITS.</p>
          <div className="flex justify-end gap-2">
            <button type="button" className="btn-secondary" onClick={() => setShowTransfer(false)}>Cancel</button>
            <button className="btn-primary" disabled={amountKobo == null || amountKobo <= 0}>Create pending transfer</button>
          </div>
        </form>
      </Modal>
    </div>
  )
}
