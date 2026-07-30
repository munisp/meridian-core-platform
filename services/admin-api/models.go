package main

// ---------- tenancy & identity ----------

type Tenant struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Isolation   string `json:"isolation"` // enclave | schema | row
	Status      string `json:"status"`    // active | suspended
	ContactMail string `json:"contact_email"`
	CreatedAt   string `json:"created_at"`
	Notes       string `json:"notes,omitempty"`
}

type User struct {
	ID        string   `json:"id"`
	Email     string   `json:"email"`
	Name      string   `json:"name"`
	Roles     []string `json:"roles"` // admin | board | operator | auditor
	TenantID  string   `json:"tenant_id"`
	Status    string   `json:"status"` // active | disabled
	CreatedAt string   `json:"created_at"`
	// Password is dev-only, never serialised
	Password string `json:"-"`
}

type Relation struct {
	Object   string `json:"object"`   // e.g. matter:lagos-v-abc
	Relation string `json:"relation"` // e.g. counsel
	Subject  string `json:"subject"`  // e.g. user:amina@chambers.ng
	Plane    string `json:"plane"`
}

// ---------- service registry ----------

type ServiceEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Plane       string   `json:"plane"` // core | compliance | inclusion | gov
	Kind        string   `json:"kind"`  // service | app | console
	TItems      []string `json:"t_items"`
	Version     string   `json:"version"`
	BaseURL     string   `json:"base_url"`
	HealthPath  string   `json:"health_path"`
	URLEnv      string   `json:"url_env"` // env var overriding BaseURL
	Enabled     bool     `json:"enabled"`
	Description string   `json:"description,omitempty"`

	// populated at runtime by the health rollup
	HealthStatus string `json:"health_status"` // ok | degraded | unreachable | disabled | unknown
	HealthDetail string `json:"health_detail,omitempty"`
	LatencyMs    int64  `json:"latency_ms,omitempty"`
	CheckedAt    string `json:"checked_at,omitempty"`
}

// ---------- rule packs / gates / audit / evidence ----------

type PackSummary struct {
	ID                string `json:"id"`
	LatestVersion     string `json:"latest_version"`
	Status            string `json:"status"`
	EffectiveFrom     string `json:"effective_from"`
	Signed            bool   `json:"signed"`
	SourceCitation    string `json:"source_citation"`
	SubjectToRegazett bool   `json:"subject_to_regazette"`
	StaleConsumers    int    `json:"stale_consumers"`
}

type Gate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	State       bool   `json:"state"` // true = open/enabled
	ArmedBy     string `json:"armed_by,omitempty"`
	UpdatedAt   string `json:"updated_at"`
	Source      string `json:"source"` // live | dev-seed
}

type AuditEvent struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Subject   string `json:"subject"`
	Actor     string `json:"actor"`
	Action    string `json:"action"`
	Detail    string `json:"detail,omitempty"`
	TenantID  string `json:"tenant_id,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
	RulePack  string `json:"rule_pack_version,omitempty"`
	Timestamp string `json:"timestamp"`
}

type EvidenceObject struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // receipt | pack-archive | corrected-record | tat
	SHA256    string `json:"sha256"`
	WORMURI   string `json:"worm_uri"`
	SizeBytes int    `json:"size_bytes"`
	Content   string `json:"content"` // dev: inline payload so the console can verify sha256 in-browser
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
	Immutable bool   `json:"immutable"`
}

// ---------- cross-zone flows ----------

type FlowDef struct {
	ID        string `json:"id"` // F1..F10
	Name      string `json:"name"`
	Direction string `json:"direction"` // market→sovereign | sovereign→market
	Payload   string `json:"payload"`
	Topics    string `json:"topics"`
	Allowed   bool   `json:"allowed"`
	Note      string `json:"note"`
}

type FlowReceipt struct {
	ID          string `json:"id"`
	Flow        string `json:"flow"` // F1..F8 (F9/F10 must never appear)
	Correlation string `json:"correlation_id"`
	Sender      string `json:"sender"`
	WORMURI     string `json:"worm_uri"`
	SHA256      string `json:"sha256"`
	Status      string `json:"status"` // accepted | rejected
	Detail      string `json:"detail,omitempty"`
	Timestamp   string `json:"timestamp"`
}

// ---------- ledger (dev-seed views; live data proxied to ledger svc) ----------

type LedgerAccount struct {
	ID       string `json:"id"`
	Ledger   int    `json:"ledger"`
	Code     int    `json:"code"`
	Owner    string `json:"owner"`
	Currency string `json:"currency"`
	Balance  int64  `json:"balance_kobo"`
	Flags    string `json:"flags,omitempty"`
}

type LedgerTransfer struct {
	ID              string `json:"id"`
	DebitAccountID  string `json:"debit_account_id"`
	CreditAccountID string `json:"credit_account_id"`
	AmountKobo      int64  `json:"amount_kobo"`
	Ledger          int    `json:"ledger"`
	Code            int    `json:"code"`
	State           string `json:"state"` // pending | posted | voided
	CreatedAt       string `json:"created_at"`
}

type ReconBreak struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Expected int64  `json:"expected_kobo"`
	Actual   int64  `json:"actual_kobo"`
	Detail   string `json:"detail"`
	Status   string `json:"status"` // open | resolved
	OpenedAt string `json:"opened_at"`
}

// ---------- workflows ----------

type WorkflowDef struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Plane       string `json:"plane"`
	Description string `json:"description"`
	Triggerable bool   `json:"triggerable"`
}

type WorkflowRun struct {
	ID         string `json:"id"`
	WorkflowID string `json:"workflow_id"`
	Status     string `json:"status"` // completed | running | failed
	Triggered  string `json:"triggered_by"`
	Input      string `json:"input,omitempty"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// ---------- settings ----------

type APIKey struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	Scopes     string `json:"scopes"`
	CreatedAt  string `json:"created_at"`
	Revoked    bool   `json:"revoked"`
	SecretTail string `json:"secret_tail"`
}

type NotifProvider struct {
	Channel  string `json:"channel"` // sms | ussd | email | push
	Provider string `json:"provider"`
	Mode     string `json:"mode"` // simulator | live
	Status   string `json:"status"`
}

type RouteRow struct {
	Plane   string `json:"plane"`
	Path    string `json:"path"`
	Upstream string `json:"upstream"`
	Methods string `json:"methods"`
	Auth    string `json:"auth"`
}
