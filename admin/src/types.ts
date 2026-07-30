export interface ServiceEntry {
  id: string
  name: string
  plane: 'core' | 'compliance' | 'inclusion' | 'gov'
  kind: 'service' | 'app' | 'console'
  t_items: string[]
  version: string
  base_url: string
  enabled: boolean
  description?: string
  health_status: 'ok' | 'degraded' | 'unreachable' | 'disabled' | 'unknown'
  health_detail?: string
  latency_ms?: number
}

export interface PackSummary {
  id: string
  latest_version: string
  status: string
  effective_from: string
  signed: boolean
  source_citation: string
  subject_to_regazette: boolean
  stale_consumers: number
}

export interface Gate {
  id: string
  name: string
  description: string
  state: boolean
  armed_by?: string
  updated_at: string
  source: string
}

export interface Tenant {
  id: string
  name: string
  slug: string
  isolation: 'enclave' | 'schema' | 'row'
  status: string
  contact_email: string
  created_at: string
  notes?: string
}

export interface User {
  id: string
  email: string
  name: string
  roles: string[]
  tenant_id: string
  status: string
  created_at: string
}

export interface AuditEvent {
  id: string
  type: string
  subject: string
  actor: string
  action: string
  detail?: string
  timestamp: string
  rule_pack_version?: string
}

export interface EvidenceObject {
  id: string
  kind: string
  sha256: string
  worm_uri: string
  size_bytes: number
  content: string
  created_by: string
  created_at: string
  immutable: boolean
}

export interface FlowDef {
  id: string
  name: string
  direction: string
  payload: string
  topics: string
  allowed: boolean
  note: string
}

export interface FlowReceipt {
  id: string
  flow: string
  correlation_id: string
  sender: string
  worm_uri: string
  sha256: string
  status: string
  detail?: string
  timestamp: string
}

export interface LedgerAccount {
  id: string
  ledger: number
  code: number
  owner: string
  currency: string
  balance_kobo: number
  flags?: string
}

export interface ReconBreak {
  id: string
  kind: string
  expected_kobo: number
  actual_kobo: number
  detail: string
  status: string
  opened_at: string
}

export interface WorkflowDef {
  id: string
  name: string
  plane: string
  description: string
  triggerable: boolean
}

export interface WorkflowRun {
  id: string
  workflow_id: string
  status: string
  triggered_by: string
  input?: string
  started_at: string
  finished_at?: string
}

export interface Overview {
  packs: { count: number; source: string }
  tenants: { count: number; source: string }
  workflows: { count: number; recent_runs: number; source: string }
  transfers: { count: number; source: string }
  evidence_objects: { count: number; source: string }
  gates: Record<string, boolean>
  services: { healthy: number; total: number }
  generated_at: string
}
