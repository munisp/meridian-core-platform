// In-memory mock adapter for DEMO MODE (static previews without admin-api).
// Seeded, realistic data matching src/types.ts. All mutations mutate local
// state only — nothing leaves the browser. Every response is labelled
// source: 'demo' so existing "dev seed" badges surface honestly.

import type { AxiosRequestConfig, AxiosResponse } from 'axios'
import type {
  AuditEvent, EvidenceObject, FlowDef, FlowReceipt, Gate, LedgerAccount,
  Overview, PackSummary, ReconBreak, ServiceEntry, Tenant, User, WorkflowDef, WorkflowRun,
} from '../types'
import { DEMO_EMAIL, DEMO_PASSWORD } from './demo'

const now = Date.now()
const iso = (minAgo: number) => new Date(now - minAgo * 60_000).toISOString()

/* ---------------- seeded state ---------------- */

const services: ServiceEntry[] = [
  { id: 'svc-core-ledger', name: 'core-ledger', plane: 'core', kind: 'service', t_items: ['ledger.post', 'ledger.recon'], version: '0.9.4', base_url: 'http://core-ledger:8081', enabled: true, health_status: 'ok', latency_ms: 14 },
  { id: 'svc-core-rules', name: 'core-rules', plane: 'core', kind: 'service', t_items: ['rules.evaluate'], version: '0.9.4', base_url: 'http://core-rules:8082', enabled: true, health_status: 'ok', latency_ms: 9 },
  { id: 'svc-compliance-kyc', name: 'compliance-kyc', plane: 'compliance', kind: 'service', t_items: ['kyc.screen'], version: '0.7.1', base_url: 'http://compliance-kyc:8091', enabled: true, health_status: 'degraded', health_detail: 'elevated p99 latency', latency_ms: 480 },
  { id: 'svc-inclusion-ussd', name: 'inclusion-ussd', plane: 'inclusion', kind: 'service', t_items: ['ussd.session'], version: '0.5.0', base_url: 'http://inclusion-ussd:8092', enabled: true, health_status: 'ok', latency_ms: 22 },
  { id: 'app-gov-portal', name: 'gov-portal', plane: 'gov', kind: 'app', t_items: ['gov.filing'], version: '1.2.0', base_url: 'https://gov.meridian.example', enabled: true, health_status: 'ok', latency_ms: 31 },
  { id: 'console-admin', name: 'admin-console', plane: 'gov', kind: 'console', t_items: ['admin.console'], version: '0.1.0', base_url: 'https://admin.meridian.example', enabled: true, health_status: 'unknown', health_detail: 'demo build' },
]

const packs: PackSummary[] = [
  { id: 'ng-firs-vat', latest_version: '2026.01', status: 'published', effective_from: '2026-01-01', signed: true, source_citation: 'FIRS VAT Circular 2025/14', subject_to_regazette: false, stale_consumers: 0 },
  { id: 'ng-cbn-banking', latest_version: '2025.12', status: 'published', effective_from: '2025-12-15', signed: true, source_citation: 'CBN/DIR/GEN/2025/09', subject_to_regazette: false, stale_consumers: 1 },
  { id: 'ng-lagos-lirs', latest_version: '2026.02-draft', status: 'draft', effective_from: '2026-02-01', signed: false, source_citation: 'LIRS Public Notice 2026-02', subject_to_regazette: true, stale_consumers: 0 },
]

const gates: Gate[] = [
  { id: 'regazette-lock', name: 'Regazette lock', description: 'Freeze rule-pack publication while gazette review is open', state: false, updated_at: iso(60 * 26), source: 'demo', armed_by: 'rule-steward@meridian.gov.ng' },
  { id: 'payout-freeze', name: 'Payout freeze', description: 'Halt outbound ledger transfers (incident response)', state: false, updated_at: iso(60 * 90), source: 'demo' },
  { id: 'kyc-enhanced-dd', name: 'Enhanced due diligence', description: 'Route all KYC through enhanced screening', state: true, armed_by: 'compliance@meridian.gov.ng', updated_at: iso(60 * 5), source: 'demo' },
  { id: 'ussd-readonly', name: 'USSD read-only', description: 'Disable transactional USSD menus during maintenance', state: false, updated_at: iso(60 * 300), source: 'demo' },
]

const tenants: Tenant[] = [
  { id: 'tnt-0001', name: 'FIRS Pilot — Abuja', slug: 'firs-abuja', isolation: 'schema', status: 'active', contact_email: 'pilot@firs.gov.ng', created_at: iso(60 * 24 * 120), notes: 'VAT e-invoicing pilot' },
  { id: 'tnt-0002', name: 'Lagos LIRS Sandbox', slug: 'lirs-sandbox', isolation: 'row', status: 'active', contact_email: 'sandbox@lirs.lagosstate.gov.ng', created_at: iso(60 * 24 * 45) },
  { id: 'tnt-0003', name: 'Kano State Enclave', slug: 'kano-enclave', isolation: 'enclave', status: 'suspended', contact_email: 'ict@kano.gov.ng', created_at: iso(60 * 24 * 200), notes: 'Suspended pending enclave re-key' },
]

const users: User[] = [
  { id: 'usr-0001', email: 'admin@meridian.gov.ng', name: 'Demo Administrator', roles: ['admin'], tenant_id: 'tnt-0001', status: 'active', created_at: iso(60 * 24 * 120) },
  { id: 'usr-0002', email: 'auditor@firs.gov.ng', name: 'Amina Bello', roles: ['auditor'], tenant_id: 'tnt-0001', status: 'active', created_at: iso(60 * 24 * 60) },
  { id: 'usr-0003', email: 'ops@lirs.lagosstate.gov.ng', name: 'Chinedu Okafor', roles: ['operator'], tenant_id: 'tnt-0002', status: 'active', created_at: iso(60 * 24 * 30) },
]

const relations = [
  { object: 'tenant:tnt-0001', relation: 'admin', subject: 'user:usr-0001', plane: 'gov' },
  { object: 'tenant:tnt-0001', relation: 'auditor', subject: 'user:usr-0002', plane: 'compliance' },
  { object: 'tenant:tnt-0002', relation: 'operator', subject: 'user:usr-0003', plane: 'core' },
]

const auditEvents: AuditEvent[] = [
  { id: 'evt-1001', type: 'auth.login', subject: 'user:usr-0001', actor: 'admin@meridian.gov.ng', action: 'login', detail: 'demo console sign-in', timestamp: iso(3) },
  { id: 'evt-1002', type: 'pack.publish', subject: 'pack:ng-firs-vat', actor: 'rule-steward@meridian.gov.ng', action: 'publish', detail: 'version 2026.01', timestamp: iso(140), rule_pack_version: '2026.01' },
  { id: 'evt-1003', type: 'gate.flip', subject: 'gate:kyc-enhanced-dd', actor: 'compliance@meridian.gov.ng', action: 'arm', detail: 'enhanced due diligence armed', timestamp: iso(300) },
  { id: 'evt-1004', type: 'ledger.transfer', subject: 'acct:0042', actor: 'ops@lirs.lagosstate.gov.ng', action: 'transfer', detail: '₦1,250,000.00 treasury sweep', timestamp: iso(420) },
  { id: 'evt-1005', type: 'tenant.update', subject: 'tenant:tnt-0003', actor: 'admin@meridian.gov.ng', action: 'suspend', detail: 'pending enclave re-key', timestamp: iso(1500) },
  { id: 'evt-1006', type: 'kyc.screen', subject: 'applicant:NG-88231', actor: 'compliance-kyc', action: 'screen', detail: 'PEP match — review queued', timestamp: iso(1600), rule_pack_version: '2025.12' },
]

const evidence: EvidenceObject[] = [
  { id: 'ev-0001', kind: 'rule-pack-bundle', sha256: '', worm_uri: 'worm://meridian/evidence/2026/01/ev-0001', size_bytes: 0, content: 'demo evidence: ng-firs-vat 2026.01 signed bundle manifest', created_by: 'core-rules', created_at: iso(60 * 30), immutable: true },
  { id: 'ev-0002', kind: 'kyc-decision', sha256: '', worm_uri: 'worm://meridian/evidence/2026/01/ev-0002', size_bytes: 0, content: 'demo evidence: KYC screening decision for applicant NG-88231 (PEP match)', created_by: 'compliance-kyc', created_at: iso(1610), immutable: true },
  { id: 'ev-0003', kind: 'recon-report', sha256: '', worm_uri: 'worm://meridian/evidence/2026/01/ev-0003', size_bytes: 0, content: 'demo evidence: daily reconciliation report ledger 1001 — 1 break opened', created_by: 'core-ledger', created_at: iso(60 * 12), immutable: true },
]

const flows: FlowDef[] = [
  { id: 'flow-01', name: 'invoice-intake', direction: 'inbound', payload: 'invoice.v1', topics: 'ledger.invoices', allowed: true, note: 'gov → core' },
  { id: 'flow-02', name: 'kyc-webhook', direction: 'inbound', payload: 'kyc.result.v1', topics: 'compliance.kyc', allowed: true, note: 'compliance → core' },
  { id: 'flow-03', name: 'raw-pii-export', direction: 'outbound', payload: 'pii.raw', topics: 'external.share', allowed: false, note: 'PII may never leave enclave' },
]

const flowReceipts: FlowReceipt[] = [
  { id: 'rcpt-9001', flow: 'invoice-intake', correlation_id: 'corr-5521', sender: 'gov-portal', worm_uri: 'worm://meridian/flows/rcpt-9001', sha256: 'd3m0cafe00000000000000000000000000000000000000000000000000000001', status: 'accepted', timestamp: iso(11) },
  { id: 'rcpt-9002', flow: 'kyc-webhook', correlation_id: 'corr-5530', sender: 'compliance-kyc', worm_uri: 'worm://meridian/flows/rcpt-9002', sha256: 'd3m0cafe00000000000000000000000000000000000000000000000000000002', status: 'accepted', timestamp: iso(55) },
]

const ledgerAccounts: LedgerAccount[] = [
  { id: 'acct-0001', ledger: 1001, code: 1000, owner: 'FIRS Pilot — Abuja', currency: 'NGN', balance_kobo: 4_820_500_000, flags: 'treasury' },
  { id: 'acct-0002', ledger: 1001, code: 2000, owner: 'Lagos LIRS Sandbox', currency: 'NGN', balance_kobo: 1_115_750_050 },
  { id: 'acct-0003', ledger: 1001, code: 3000, owner: 'Kano State Enclave', currency: 'NGN', balance_kobo: 250_000_000, flags: 'frozen' },
  { id: 'acct-0042', ledger: 1002, code: 1100, owner: 'Settlement float', currency: 'NGN', balance_kobo: 98_340_000 },
]

const reconBreaks: ReconBreak[] = [
  { id: 'brk-0001', kind: 'balance-mismatch', expected_kobo: 98_340_000, actual_kobo: 98_310_000, detail: 'settlement float off by ₦300.00 after retry storm', status: 'open', opened_at: iso(60 * 12) },
]

const workflowDefs: WorkflowDef[] = [
  { id: 'wf-invoice-clearance', name: 'Invoice clearance', plane: 'core', description: 'Validate, rule-check and clear an e-invoice end to end', triggerable: true },
  { id: 'wf-kyc-review', name: 'KYC manual review', plane: 'compliance', description: 'Route flagged applicants to an analyst queue', triggerable: true },
  { id: 'wf-nightly-recon', name: 'Nightly reconciliation', plane: 'core', description: 'Scheduled ledger reconciliation (cron)', triggerable: false },
]

const workflowRuns: WorkflowRun[] = [
  { id: 'run-7001', workflow_id: 'wf-invoice-clearance', status: 'completed', triggered_by: 'gov-portal', input: '{"invoice":"INV-10231"}', started_at: iso(20), finished_at: iso(19) },
  { id: 'run-7002', workflow_id: 'wf-kyc-review', status: 'running', triggered_by: 'compliance-kyc', input: '{"applicant":"NG-88231"}', started_at: iso(8) },
  { id: 'run-7003', workflow_id: 'wf-nightly-recon', status: 'failed', triggered_by: 'cron', started_at: iso(60 * 12), finished_at: iso(60 * 12 - 4) },
]

const settingsState = {
  flags: { 'rules.shadow-eval': true, 'ledger.dual-write': false, 'kyc.auto-escalate': true, 'ussd.beta-menu': false } as Record<string, boolean>,
  waf_mode: 'detect',
  api_keys: [
    { id: 'key-01', name: 'ci-readonly', prefix: 'mk_live_', scopes: 'read:packs read:health', created_at: iso(60 * 24 * 30), revoked: false, secret_tail: 'a1b2' },
    { id: 'key-02', name: 'old-export', prefix: 'mk_live_', scopes: 'read:packs', created_at: iso(60 * 24 * 90), revoked: true, secret_tail: 'z9y8' },
  ] as { id: string; name: string; prefix: string; scopes: string; created_at: string; revoked: boolean; secret_tail: string }[],
  providers: [
    { channel: 'email', provider: 'ses', mode: 'live', status: 'ok' },
    { channel: 'sms', provider: 'africastalking', mode: 'sandbox', status: 'degraded' },
    { channel: 'ussd', provider: 'vas2nets', mode: 'live', status: 'ok' },
  ],
  routes: [
    { plane: 'core', path: '/v1/ledger/*', upstream: 'core-ledger:8081', methods: 'GET,POST', auth: 'jwt' },
    { plane: 'compliance', path: '/v1/kyc/*', upstream: 'compliance-kyc:8091', methods: 'GET,POST', auth: 'jwt+mfa' },
    { plane: 'gov', path: '/v1/admin/*', upstream: 'admin-api:8095', methods: 'GET,POST,PUT', auth: 'jwt' },
  ],
}

/* ---------------- helpers ---------------- */

function b64url(s: string): string {
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/** Fake JWT, unmistakably marked as demo — never valid against a live API. */
export function demoToken(): string {
  const header = b64url(JSON.stringify({ alg: 'none', typ: 'JWT', demo: true }))
  const payload = b64url(
    JSON.stringify({
      sub: 'usr-0001',
      email: DEMO_EMAIL,
      name: 'Demo Administrator',
      roles: ['admin'],
      iss: 'meridian-demo-mode',
      demo: true,
      iat: Math.floor(now / 1000),
      exp: Math.floor(now / 1000) + 8 * 3600,
    }),
  )
  return `${header}.${payload}.DEMO-SIGNATURE-NOT-VALID`
}

async function ensureEvidenceHashes() {
  for (const ev of evidence) {
    if (!ev.sha256) {
      const buf = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(ev.content))
      ev.sha256 = Array.from(new Uint8Array(buf)).map((b) => b.toString(16).padStart(2, '0')).join('')
      ev.size_bytes = new TextEncoder().encode(ev.content).length
    }
  }
}

class HttpError extends Error {
  constructor(public status: number, public detail: string) {
    super(detail)
  }
}

const json = (s: string) => (s ? JSON.parse(s) : {})

/** Route a request against the seeded state. Throws HttpError for misses. */
async function route(method: string, path: string, query: URLSearchParams, body: any): Promise<unknown> {
  // auth
  if (method === 'post' && path === '/v1/admin/login') {
    if (body.email?.toLowerCase() === DEMO_EMAIL && body.password === DEMO_PASSWORD) {
      return {
        token: demoToken(),
        user: { id: 'usr-0001', email: DEMO_EMAIL, name: 'Demo Administrator', roles: ['admin'] },
        demo: true,
      }
    }
    throw new HttpError(401, 'Invalid demo credentials — use the demo credentials shown below.')
  }

  // dashboard
  if (method === 'get' && path === '/v1/admin/overview') {
    const ov: Overview = {
      packs: { count: packs.length, source: 'demo' },
      tenants: { count: tenants.length, source: 'demo' },
      workflows: { count: workflowDefs.length, recent_runs: workflowRuns.length, source: 'demo' },
      transfers: { count: 4, source: 'demo' },
      evidence_objects: { count: evidence.length, source: 'demo' },
      gates: Object.fromEntries(gates.map((g) => [g.id, g.state])),
      services: { healthy: services.filter((s) => s.health_status === 'ok').length, total: services.length },
      generated_at: new Date().toISOString(),
    }
    return ov
  }

  // applications / services
  if (method === 'get' && path === '/v1/admin/services') return { services, source: 'demo' }
  let m = path.match(/^\/v1\/admin\/services\/([^/]+)\/toggle$/)
  if (method === 'post' && m) {
    const s = services.find((x) => x.id === m![1])
    if (!s) throw new HttpError(404, 'service not found')
    s.enabled = !s.enabled
    s.health_status = s.enabled ? 'ok' : 'disabled'
    return { service: s, source: 'demo' }
  }

  // audit + evidence
  if (method === 'get' && path === '/v1/admin/audit/events') {
    const subject = query.get('subject')?.toLowerCase()
    const type = query.get('type')?.toLowerCase()
    const events = auditEvents.filter(
      (e) => (!subject || e.subject.toLowerCase().includes(subject)) && (!type || e.type.toLowerCase().includes(type)),
    )
    return { events, source: 'demo' }
  }
  if (method === 'get' && path === '/v1/admin/evidence') {
    await ensureEvidenceHashes()
    return { evidence, source: 'demo' }
  }
  m = path.match(/^\/v1\/admin\/evidence\/([^/]+)$/)
  if (method === 'get' && m) {
    await ensureEvidenceHashes()
    const ev = evidence.find((x) => x.id === m![1])
    if (!ev) throw new HttpError(404, 'evidence not found')
    return { evidence: ev, source: 'demo' }
  }
  if (method === 'post' && path === '/v1/admin/tat/assemble') {
    const subject = String(body.subject || '')
    const entries = auditEvents
      .filter((e) => !subject || e.subject.includes(subject) || e.actor.includes(subject))
      .map((e) => ({ timestamp: e.timestamp, actor: e.actor, action: e.action, detail: e.detail || '', hash: e.id }))
    return { entries, source: 'demo' }
  }

  // flows
  if (method === 'get' && path === '/v1/admin/flows/matrix') return { flows, source: 'demo' }
  if (method === 'get' && path === '/v1/admin/flows/receipts') return { receipts: flowReceipts, source: 'demo' }
  if (method === 'get' && path === '/v1/admin/flows/forbidden') return { status: 'clean', sightings: [], source: 'demo' }

  // gates + gazette
  if (method === 'get' && path === '/v1/admin/gates') return { gates, source: 'demo' }
  m = path.match(/^\/v1\/admin\/gates\/([^/]+)\/flip$/)
  if (method === 'post' && m) {
    const g = gates.find((x) => x.id === m![1])
    if (!g) throw new HttpError(404, 'gate not found')
    if (!body.confirm) throw new HttpError(400, 'confirmation required')
    g.state = !g.state
    g.armed_by = DEMO_EMAIL
    g.updated_at = new Date().toISOString()
    auditEvents.unshift({ id: `evt-${1000 + auditEvents.length + 1}`, type: 'gate.flip', subject: `gate:${g.id}`, actor: DEMO_EMAIL, action: g.state ? 'arm' : 'disarm', detail: body.reason || 'demo flip', timestamp: new Date().toISOString() })
    return { gate: g, source: 'demo' }
  }
  if (method === 'get' && path === '/v1/admin/gazette-watch') {
    return {
      watch: [
        { instrument: 'LIRS Public Notice 2026-02', status: 'pending-gazette', gate: 'regazette-lock', checked_at: iso(60 * 3) },
        { instrument: 'CBN/DIR/GEN/2025/09', status: 'gazetted', gate: 'regazette-lock', checked_at: iso(60 * 26) },
      ],
      source: 'demo',
    }
  }

  // ledger
  if (method === 'get' && path === '/v1/admin/ledger/accounts') return { accounts: ledgerAccounts, source: 'demo' }
  if (method === 'get' && path === '/v1/admin/ledger/recon-breaks') return { breaks: reconBreaks, source: 'demo' }
  m = path.match(/^\/v1\/admin\/ledger\/accounts\/([^/]+)\/balance$/)
  if (method === 'get' && m) {
    const id = decodeURIComponent(m[1])
    const a = ledgerAccounts.find((x) => x.id === id || String(x.code) === id)
    if (!a) throw new HttpError(404, 'account not found')
    return { account_id: a.id, balance_kobo: a.balance_kobo, currency: a.currency, source: 'demo' }
  }
  if (method === 'post' && path === '/v1/admin/ledger/transfers') {
    const debit = ledgerAccounts.find((x) => x.id === body.debit_account_id)
    const credit = ledgerAccounts.find((x) => x.id === body.credit_account_id)
    const amt = Number(body.amount_kobo)
    if (!debit || !credit) throw new HttpError(404, 'debit or credit account not found')
    if (!amt || amt <= 0) throw new HttpError(400, 'amount_kobo must be positive')
    if (debit.balance_kobo < amt) throw new HttpError(409, 'insufficient funds (demo)')
    debit.balance_kobo -= amt
    credit.balance_kobo += amt
    const id = `txn-${Math.floor(1000 + Math.random() * 9000)}`
    auditEvents.unshift({ id: `evt-${1000 + auditEvents.length + 1}`, type: 'ledger.transfer', subject: debit.id, actor: DEMO_EMAIL, action: 'transfer', detail: `${id} ${debit.id} → ${credit.id}`, timestamp: new Date().toISOString() })
    return { id, status: 'posted', source: 'demo' }
  }

  // rule packs
  if (method === 'get' && path === '/v1/admin/packs') {
    return { packs, source: 'demo', stale_consumers: packs.filter((p) => p.stale_consumers > 0).map((p) => p.id) }
  }
  m = path.match(/^\/v1\/admin\/packs\/([^/]+)\/([^/]+)\/publish$/)
  if (method === 'post' && m) {
    const p = packs.find((x) => x.id === m![1])
    if (!p) throw new HttpError(404, 'pack not found')
    p.status = 'published'
    p.signed = true
    p.latest_version = m[2]
    auditEvents.unshift({ id: `evt-${1000 + auditEvents.length + 1}`, type: 'pack.publish', subject: `pack:${p.id}`, actor: DEMO_EMAIL, action: 'publish', detail: `version ${m[2]} (demo)`, timestamp: new Date().toISOString(), rule_pack_version: m[2] })
    return { pack: p, source: 'demo' }
  }
  m = path.match(/^\/v1\/admin\/packs\/([^/]+)$/)
  if (method === 'get' && m) {
    const p = packs.find((x) => x.id === m![1])
    if (!p) throw new HttpError(404, 'pack not found')
    return {
      summary: p,
      yaml: `# DEMO rule pack ${p.id} ${p.latest_version}\nversion: "${p.latest_version}"\neffective_from: "${p.effective_from}"\nsource: "${p.source_citation}"\nrules:\n  - id: demo-rule-1\n    when: "amount_kobo > 0"\n    then: "apply_standard_rate"\n`,
      signature: { algorithm: 'ed25519-demo', key_id: 'demo-key-01', verified: p.signed },
      source: 'demo',
    }
  }

  // settings
  if (method === 'get' && path === '/v1/admin/settings/flags') return { flags: settingsState.flags, source: 'demo' }
  if (method === 'put' && path === '/v1/admin/settings/flags') {
    Object.assign(settingsState.flags, body.flags || {})
    return { flags: settingsState.flags, source: 'demo' }
  }
  if (method === 'get' && path === '/v1/admin/settings/api-keys') return { api_keys: settingsState.api_keys, source: 'demo' }
  if (method === 'post' && path === '/v1/admin/settings/api-keys') {
    const id = `key-${String(settingsState.api_keys.length + 1).padStart(2, '0')}`
    settingsState.api_keys.push({ id, name: body.name || id, prefix: 'mk_demo_', scopes: body.scopes || '', created_at: new Date().toISOString(), revoked: false, secret_tail: 'd3m0' })
    return { id, secret_once: 'mk_demo_NOT-A-REAL-KEY-d3m0', source: 'demo' }
  }
  m = path.match(/^\/v1\/admin\/settings\/api-keys\/([^/]+)\/revoke$/)
  if (method === 'post' && m) {
    const k = settingsState.api_keys.find((x) => x.id === m![1])
    if (k) k.revoked = true
    return { revoked: true, source: 'demo' }
  }
  if (method === 'get' && path === '/v1/admin/settings/notifications') return { providers: settingsState.providers, source: 'demo' }
  if (method === 'get' && path === '/v1/admin/settings/routes') return { routes: settingsState.routes, waf_mode: settingsState.waf_mode, source: 'demo' }
  if (method === 'post' && path === '/v1/admin/settings/waf-mode') {
    settingsState.waf_mode = body.mode || 'detect'
    return { waf_mode: settingsState.waf_mode, source: 'demo' }
  }

  // tenants / users / identity
  if (method === 'get' && path === '/v1/admin/tenants') return tenants
  if (method === 'post' && path === '/v1/admin/tenants') {
    const id = `tnt-${String(tenants.length + 1).padStart(4, '0')}`
    const t: Tenant = {
      id,
      name: body.name || id,
      slug: (body.name || id).toLowerCase().replace(/[^a-z0-9]+/g, '-'),
      isolation: body.isolation || 'row',
      status: 'active',
      contact_email: body.contact_email || '',
      created_at: new Date().toISOString(),
    }
    tenants.push(t)
    return t
  }
  m = path.match(/^\/v1\/admin\/tenants\/([^/]+)$/)
  if (method === 'put' && m) {
    const t = tenants.find((x) => x.id === m![1])
    if (!t) throw new HttpError(404, 'tenant not found')
    Object.assign(t, body)
    return t
  }
  if (method === 'get' && path === '/v1/admin/users') return users
  if (method === 'post' && path === '/v1/admin/users') {
    const u: User = {
      id: `usr-${String(users.length + 1).padStart(4, '0')}`,
      email: body.email || '',
      name: body.name || '',
      roles: body.roles || ['operator'],
      tenant_id: body.tenant_id || tenants[0]?.id || '',
      status: 'active',
      created_at: new Date().toISOString(),
    }
    users.push(u)
    return u
  }
  if (method === 'get' && path === '/v1/admin/identity/relations') return relations

  // workflows
  if (method === 'get' && path === '/v1/admin/workflows') return { workflows: workflowDefs, source: 'demo' }
  if (method === 'get' && path === '/v1/admin/workflow-runs') return { runs: workflowRuns, source: 'demo' }
  m = path.match(/^\/v1\/admin\/workflows\/([^/]+)\/trigger$/)
  if (method === 'post' && m) {
    const def = workflowDefs.find((x) => x.id === m![1])
    if (!def) throw new HttpError(404, 'workflow not found')
    if (!def.triggerable) throw new HttpError(409, 'workflow is not manually triggerable')
    const run: WorkflowRun = {
      id: `run-${7000 + workflowRuns.length + 1}`,
      workflow_id: def.id,
      status: 'completed',
      triggered_by: DEMO_EMAIL,
      input: body.input,
      started_at: new Date().toISOString(),
      finished_at: new Date().toISOString(),
    }
    workflowRuns.unshift(run)
    return { run, mode: 'demo', source: 'demo' }
  }

  throw new HttpError(404, `demo mock: no route for ${method.toUpperCase()} ${path}`)
}

/** Axios adapter serving all admin-api endpoints from seeded in-memory data. */
export async function mockAdapter(config: AxiosRequestConfig): Promise<AxiosResponse> {
  const url = new URL(config.url || '/', 'http://demo.local')
  const method = (config.method || 'get').toLowerCase()
  // tiny latency so loading states are visible
  await new Promise((r) => setTimeout(r, 120))
  try {
    const data = await route(method, url.pathname, url.searchParams, json(config.data as string))
    return { data, status: 200, statusText: 'OK', headers: {}, config: config as any }
  } catch (e) {
    if (e instanceof HttpError) {
      return Promise.reject({
        response: { data: { detail: e.detail }, status: e.status },
        config,
        message: e.detail,
      })
    }
    throw e
  }
}
