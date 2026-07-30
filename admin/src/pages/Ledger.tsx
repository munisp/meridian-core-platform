import { FormEvent, useEffect, useState } from 'react'
import { api, fmtKobo, fmtTime } from '../api'
import { LedgerAccount, ReconBreak } from '../types'
import { Badge, DevSeedTag, Modal, PageHeader } from '../components'

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
  const [form, setForm] = useState({ debit_account_id: '', credit_account_id: '', amount: '', ledger: 200 })
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
        amount_kobo: Math.round(parseFloat(form.amount) * 100),
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
      {msg && <div className="mb-4 rounded-lg bg-moss-50 border border-moss-200 px-4 py-2.5 text-sm text-moss-800">{msg}</div>}

      <form onSubmit={doLookup} className="mb-6 flex gap-2 max-w-xl">
        <input className="input font-mono text-xs" placeholder="Account id e.g. 200|5|psm-settlement-pssp" value={lookup} onChange={(e) => setLookup(e.target.value)} />
        <button className="btn-secondary shrink-0">Balance lookup</button>
      </form>
      {lookupResult && <div className="mb-6 font-mono text-sm text-sand-800">{lookupResult}</div>}

      <div className="card overflow-x-auto mb-8">
        <table className="w-full">
          <thead>
            <tr><th className="th">Account</th><th className="th">Ledger</th><th className="th">Owner</th><th className="th">Flags</th><th className="th text-right">Balance</th></tr>
          </thead>
          <tbody>
            {accounts.map((a) => (
              <tr key={a.id} className="hover:bg-sand-50">
                <td className="td font-mono text-xs">{a.id}</td>
                <td className="td text-xs">{a.ledger} · {LEDGER_NAMES[a.ledger] || ''}</td>
                <td className="td text-xs">{a.owner}</td>
                <td className="td">{a.flags ? <Badge tone="amber">{a.flags}</Badge> : <span className="text-xs text-sand-400">—</span>}</td>
                <td className="td text-right font-mono text-sm">{fmtKobo(a.balance_kobo)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <section>
        <h2 className="text-sm font-semibold text-sand-900 mb-3">Recon breaks (PSSP 3-way)</h2>
        <div className="card overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr><th className="th">Break</th><th className="th">Kind</th><th className="th text-right">Expected</th><th className="th text-right">Actual</th><th className="th">Detail</th><th className="th">Status</th></tr>
            </thead>
            <tbody>
              {breaks.map((b) => (
                <tr key={b.id} className="hover:bg-sand-50">
                  <td className="td font-mono text-xs">{b.id}<div className="text-sand-400">{fmtTime(b.opened_at)}</div></td>
                  <td className="td text-xs">{b.kind}</td>
                  <td className="td text-right font-mono text-xs">{fmtKobo(b.expected_kobo)}</td>
                  <td className="td text-right font-mono text-xs">{fmtKobo(b.actual_kobo)}</td>
                  <td className="td text-xs max-w-sm">{b.detail}</td>
                  <td className="td"><Badge tone={b.status === 'open' ? 'red' : 'green'}>{b.status}</Badge></td>
                </tr>
              ))}
              {breaks.length === 0 && <tr><td className="td text-center text-sand-400" colSpan={6}>No recon breaks.</td></tr>}
            </tbody>
          </table>
        </div>
      </section>

      <Modal open={showTransfer} title="Initiate transfer (pending / two-phase)" onClose={() => setShowTransfer(false)}>
        <form onSubmit={submitTransfer} className="space-y-4">
          <div>
            <label className="label">Debit account</label>
            <input className="input font-mono text-xs" required value={form.debit_account_id} onChange={(e) => setForm({ ...form, debit_account_id: e.target.value })} placeholder="100|4|op-float-lagos-01" />
          </div>
          <div>
            <label className="label">Credit account</label>
            <input className="input font-mono text-xs" required value={form.credit_account_id} onChange={(e) => setForm({ ...form, credit_account_id: e.target.value })} placeholder="200|5|psm-settlement-pssp" />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="label">Amount (₦)</label>
              <input className="input" type="number" min="0.01" step="0.01" required value={form.amount} onChange={(e) => setForm({ ...form, amount: e.target.value })} />
            </div>
            <div>
              <label className="label">Ledger</label>
              <select className="input" value={form.ledger} onChange={(e) => setForm({ ...form, ledger: Number(e.target.value) })}>
                {Object.entries(LEDGER_NAMES).map(([k, v]) => <option key={k} value={k}>{k} · {v}</option>)}
              </select>
            </div>
          </div>
          <p className="text-xs text-sand-500">Creates a pending (authorise) transfer. Post or void via the ledger service; float accounts enforce DEBITS_MUST_NOT_EXCEED_CREDITS.</p>
          <div className="flex justify-end gap-2">
            <button type="button" className="btn-secondary" onClick={() => setShowTransfer(false)}>Cancel</button>
            <button className="btn-primary">Create pending transfer</button>
          </div>
        </form>
      </Modal>
    </div>
  )
}
