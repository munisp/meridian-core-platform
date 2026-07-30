/** Meridian envelope + nrs.*.v1 payload types (SPEC 2 packages/schemas). */
export interface Envelope<T = unknown> {
  id: string; // ULID, 26 chars
  type: string; // nrs.<family>.<name>.v1
  source: string;
  time: string; // RFC3339
  tenant_id: string;
  trace_id: string;
  rule_pack_version: string; // "rp-xxx@1.2.0" or ""
  data: T;
}

export interface PaymentEvent {
  reference: string;
  amount_kobo: number; // integer kobo, never float naira
  tin_hash?: string;
  band?: string;
  state?: string;
  certificate_serial?: string;
}

export interface RulePackPublished {
  pack_id: string;
  version: string;
  ref: string;
  sha256: string;
  effective_from: string;
  subject_to_regazette: boolean;
  published_by: string;
}

export interface LedgerTransferEvent {
  transfer_id: string;
  debit_account_id: string;
  credit_account_id: string;
  amount_kobo: number;
  ledger: number;
  code: number;
  pending: boolean;
}

export interface CaseFeedEvent {
  case_id: string;
  pseudo_tin: string;
  score: number;
  reasons: string[];
}
