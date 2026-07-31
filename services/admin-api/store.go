package main

import "sync"

// Store holds all admin-plane state. Dev mode: in-memory, seeded at boot.
// Live data for packs/gates/audit/ledger/evidence is fetched from the owning
// core services when reachable; these maps are the graceful-degradation
// fallback ("dev seed") and the local store for admin-owned resources
// (tenants, users, flags, api keys, service registry, flow receipts).

type Store struct {
	mu sync.Mutex

	Tenants  map[string]*Tenant
	Users    map[string]*User // keyed by email
	Services map[string]*ServiceEntry

	Gates   map[string]*Gate
	Receipts []FlowReceipt
	Forbidden []FlowReceipt // any F9/F10 sightings (must always be empty)

	AuditEvents []AuditEvent
	Evidence    map[string]*EvidenceObject

	LedgerAccounts  map[string]*LedgerAccount
	LedgerTransfers map[string]*LedgerTransfer
	ReconBreaks     []ReconBreak

	WorkflowDefs []WorkflowDef
	WorkflowRuns []WorkflowRun

	Flags    map[string]bool
	APIKeys  []APIKey
	Relations []Relation
	NotifProviders []NotifProvider
	Routes   []RouteRow
	WAFMode  string // detect | enforce
}

func NewStore() *Store {
	s := &Store{
		Tenants:         map[string]*Tenant{},
		Users:           map[string]*User{},
		Services:        map[string]*ServiceEntry{},
		Gates:           map[string]*Gate{},
		Evidence:        map[string]*EvidenceObject{},
		LedgerAccounts:  map[string]*LedgerAccount{},
		LedgerTransfers: map[string]*LedgerTransfer{},
		Flags:           map[string]bool{},
		WAFMode:         "detect",
	}
	seedTenantsUsers(s)
	seedServices(s)
	seedPacksGatesFlows(s)
	seedAuditEvidence(s)
	seedLedger(s)
	seedWorkflows(s)
	seedSettings(s)
	return s
}

func seedTenantsUsers(s *Store) {
	s.Tenants["t-nrs-sovereign"] = &Tenant{ID: "t-nrs-sovereign", Name: "NRS Sovereign Enclave", Slug: "nrs-sovereign", Isolation: "enclave", Status: "active", ContactMail: "cto@nrs.gov.ng", CreatedAt: "2025-01-05T09:00:00Z", Notes: "Sovereign zone control tenant; only pseudonymised data leaves."}
	s.Tenants["t-lagos-lirs"] = &Tenant{ID: "t-lagos-lirs", Name: "Lagos Internal Revenue Service", Slug: "lagos-lirs", Isolation: "schema", Status: "active", ContactMail: "admin@lirs.gov.ng", CreatedAt: "2025-02-11T10:30:00Z"}
	s.Tenants["t-kano-sirs"] = &Tenant{ID: "t-kano-sirs", Name: "Kano State IRS", Slug: "kano-sirs", Isolation: "schema", Status: "active", ContactMail: "ict@kanoirs.gov.ng", CreatedAt: "2025-03-02T08:15:00Z"}
	s.Tenants["t-acme-bank"] = &Tenant{ID: "t-acme-bank", Name: "Acme Merchant Bank (PSP)", Slug: "acme-bank", Isolation: "row", Status: "active", ContactMail: "integration@acmebank.ng", CreatedAt: "2025-04-18T14:45:00Z"}
	s.Tenants["t-northern-retail"] = &Tenant{ID: "t-northern-retail", Name: "Northern Retail Cooperative", Slug: "northern-retail", Isolation: "row", Status: "suspended", ContactMail: "ops@northernretail.ng", CreatedAt: "2025-05-30T11:05:00Z", Notes: "Suspended pending KYC refresh."}

	s.Users["admin@meridian.local"] = &User{ID: "u-admin", Email: "admin@meridian.local", Name: "Platform Administrator", Roles: []string{"admin", "board"}, TenantID: "t-nrs-sovereign", Status: "active", CreatedAt: "2025-01-05T09:00:00Z", PasswordHash: MustHashPassword("admin123")}
	s.Users["operator@meridian.local"] = &User{ID: "u-operator", Email: "operator@meridian.local", Name: "Console Operator", Roles: []string{"operator"}, TenantID: "t-nrs-sovereign", Status: "active", CreatedAt: "2025-01-06T09:00:00Z", PasswordHash: MustHashPassword("operator123")}
	s.Users["auditor@meridian.local"] = &User{ID: "u-auditor", Email: "auditor@meridian.local", Name: "Independent Auditor", Roles: []string{"auditor"}, TenantID: "t-nrs-sovereign", Status: "active", CreatedAt: "2025-01-06T09:30:00Z", PasswordHash: MustHashPassword("auditor123")}
	s.Users["amina@chambers.ng"] = &User{ID: "u-amina", Email: "amina@chambers.ng", Name: "Amina Bello (Practitioner)", Roles: []string{"operator"}, TenantID: "t-acme-bank", Status: "active", CreatedAt: "2025-04-20T10:00:00Z", PasswordHash: MustHashPassword("changeme123")}

	s.Relations = []Relation{
		{Object: "matter:lagos-v-abc-holdings", Relation: "counsel", Subject: "user:amina@chambers.ng", Plane: "compliance"},
		{Object: "doc:cbcr-2024-abc-holdings", Relation: "privileged", Subject: "matter:lagos-v-abc-holdings", Plane: "compliance"},
		{Object: "tenant:t-lagos-lirs", Relation: "member", Subject: "user:admin@meridian.local", Plane: "core"},
		{Object: "tenant:t-lagos-lirs", Relation: "auditor", Subject: "user:auditor@meridian.local", Plane: "core"},
		{Object: "case:ombud-2025-014", Relation: "clerk", Subject: "user:operator@meridian.local", Plane: "gov"},
		{Object: "case:ombud-2025-014", Relation: "member", Subject: "user:auditor@meridian.local", Plane: "gov"},
	}
}

func seedAuditEvidence(s *Store) {
	s.AuditEvents = []AuditEvent{
		{ID: "ae-001", Type: "auth.login", Subject: "user:admin@meridian.local", Actor: "admin@meridian.local", Action: "login", Detail: "dev JWT issued", Timestamp: "2025-06-24T08:02:11Z", TraceID: "4bf92f3577b34da6a3ce929d0e0e4736"},
		{ID: "ae-002", Type: "rulepack.published", Subject: "rp-wht-2024@1.0.0", Actor: "governance-board", Action: "publish", Detail: "ceremony signed ed25519 key governance-board-2026", RulePack: "rp-wht-2024@1.0.0", Timestamp: "2025-06-24T08:11:40Z"},
		{ID: "ae-003", Type: "gate.flipped", Subject: "carf.transmit_enabled", Actor: "board", Action: "flip", Detail: "state=false (armed, awaiting gazette confirmation)", Timestamp: "2025-06-24T08:20:03Z"},
		{ID: "ae-004", Type: "ledger.transfer", Subject: "transfer:tr-20250624-001", Actor: "presumptive-svc", Action: "post_pending", Detail: "PSP settlement batch 42 posted, ledger 200", Timestamp: "2025-06-24T09:04:57Z"},
		{ID: "ae-005", Type: "evidence.worm_write", Subject: "ev-ubl-receipt-0091", Actor: "enclave-gateway", Action: "seal", Detail: "F1 pre-clearance receipt sealed before consumer delivery", Timestamp: "2025-06-24T09:07:31Z"},
		{ID: "ae-006", Type: "tenant.updated", Subject: "tenant:t-northern-retail", Actor: "admin@meridian.local", Action: "suspend", Detail: "KYC refresh requested", TenantID: "t-northern-retail", Timestamp: "2025-06-24T09:15:12Z"},
		{ID: "ae-007", Type: "workflow.triggered", Subject: "wf-psm-settlement", Actor: "operator@meridian.local", Action: "trigger", Detail: "manual settlement run for PSSP batch 43", Timestamp: "2025-06-24T09:31:45Z"},
		{ID: "ae-008", Type: "crosszone.receipt", Subject: "flow:F7", Actor: "jrb-svc", Action: "serve", Detail: "attribution feed batch served to states (NTAA 30% PoC)", Timestamp: "2025-06-24T10:02:09Z"},
	}

	s.Evidence["ev-ubl-receipt-0091"] = &EvidenceObject{ID: "ev-ubl-receipt-0091", Kind: "receipt", SHA256: "", WORMURI: "worm://minio/evidence/2025/06/24/ev-ubl-receipt-0091.json", Content: `{"flow":"F1","irn":"IRN-2025-000912","supplier_tin_hash":"9f2c...","status":"pre-cleared","crypto_stamp":"ed25519:7a41..."}`, CreatedBy: "enclave-gateway", CreatedAt: "2025-06-24T09:07:31Z", Immutable: true}
	s.Evidence["ev-pack-rp-wht-2024-1.0.0"] = &EvidenceObject{ID: "ev-pack-rp-wht-2024-1.0.0", Kind: "pack-archive", SHA256: "", WORMURI: "worm://minio/evidence/packs/rp-wht-2024/1.0.0.yaml", Content: "id: rp-wht-2024\nversion: 1.0.0\nstatus: published\n# canonical YAML bytes archived by ceremony", CreatedBy: "ceremony", CreatedAt: "2025-06-24T08:11:52Z", Immutable: true}
	s.Evidence["ev-rev360-rec-4471"] = &EvidenceObject{ID: "ev-rev360-rec-4471", Kind: "corrected-record", SHA256: "", WORMURI: "worm://minio/evidence/2025/06/23/rev360-4471.json", Content: `{"defect_class":"blocked_tcc","record":"RC-4471","action":"remitted_corrected","workbench":"rev360"}`, CreatedBy: "rev360-svc", CreatedAt: "2025-06-23T16:44:02Z", Immutable: true}
}

func seedLedger(s *Store) {
	s.LedgerAccounts["100|4|op-float-lagos-01"] = &LedgerAccount{ID: "100|4|op-float-lagos-01", Ledger: 100, Code: 4, Owner: "operator:lagos-01", Currency: "NGN", Balance: 4875000000, Flags: "DEBITS_MUST_NOT_EXCEED_CREDITS"}
	s.LedgerAccounts["100|4|op-float-kano-03"] = &LedgerAccount{ID: "100|4|op-float-kano-03", Ledger: 100, Code: 4, Owner: "operator:kano-03", Currency: "NGN", Balance: 1220000000, Flags: "DEBITS_MUST_NOT_EXCEED_CREDITS"}
	s.LedgerAccounts["200|5|psm-settlement-pssp"] = &LedgerAccount{ID: "200|5|psm-settlement-pssp", Ledger: 200, Code: 5, Owner: "pssp:settlement", Currency: "NGN", Balance: 9043210000}
	s.LedgerAccounts["500|6|ombud-deposit-014"] = &LedgerAccount{ID: "500|6|ombud-deposit-014", Ledger: 500, Code: 6, Owner: "case:ombud-2025-014", Currency: "NGN", Balance: 200000000, Flags: "hold"}
	s.LedgerAccounts["600|5|jrb-attribution-lagos"] = &LedgerAccount{ID: "600|5|jrb-attribution-lagos", Ledger: 600, Code: 5, Owner: "authority:lagos-lirs", Currency: "NGN", Balance: 3114000000}
	s.LedgerAccounts["700|5|commissions-june"] = &LedgerAccount{ID: "700|5|commissions-june", Ledger: 700, Code: 5, Owner: "pool:commissions", Currency: "NGN", Balance: 66400000}

	s.LedgerTransfers["tr-20250624-001"] = &LedgerTransfer{ID: "tr-20250624-001", DebitAccountID: "200|5|psm-settlement-pssp", CreditAccountID: "600|5|jrb-attribution-lagos", AmountKobo: 145000000, Ledger: 200, Code: 2, State: "posted", CreatedAt: "2025-06-24T09:04:57Z"}
	s.LedgerTransfers["tr-20250624-002"] = &LedgerTransfer{ID: "tr-20250624-002", DebitAccountID: "100|4|op-float-lagos-01", CreditAccountID: "700|5|commissions-june", AmountKobo: 12500000, Ledger: 700, Code: 1, State: "pending", CreatedAt: "2025-06-24T10:12:33Z"}

	s.ReconBreaks = []ReconBreak{
		{ID: "rb-20250623-01", Kind: "pssp_vs_platform", Expected: 9043210000, Actual: 9041200000, Detail: "PSSP settlement file batch 41 short by ₦20,100.00; 3 intents unmatched", Status: "open", OpenedAt: "2025-06-23T17:40:00Z"},
		{ID: "rb-20250622-02", Kind: "treasury_vs_pssp", Expected: 150000000, Actual: 150000000, Detail: "rounding difference ₦0.00 — auto-cleared by tolerance rule", Status: "resolved", OpenedAt: "2025-06-22T18:02:00Z"},
	}
}

func seedWorkflows(s *Store) {
	s.WorkflowDefs = []WorkflowDef{
		{ID: "wf-mbs-preclearance", Name: "MBS pre-clearance", Plane: "compliance", Description: "Submit canonical invoice to MBS for IRN + crypto stamp (T1/T2).", Triggerable: true},
		{ID: "wf-wht-remit-schedule", Name: "WHT remittance schedule", Plane: "compliance", Description: "Periodic WHT credit aggregation and remittance file generation (T7).", Triggerable: true},
		{ID: "wf-vat-normalise", Name: "VAT POS normalise", Plane: "compliance", Description: "Normalise POS receipts into canonical baskets (T6).", Triggerable: true},
		{ID: "wf-vat-settle-match", Name: "VAT settle-match", Plane: "compliance", Description: "Match POS settlements against ledger entries (T6).", Triggerable: true},
		{ID: "wf-vat-spool-drain", Name: "VAT spool drain", Plane: "compliance", Description: "Store-and-forward drain of offline POS spool (T6).", Triggerable: true},
		{ID: "wf-etr-compute", Name: "ETR compute pipeline", Plane: "compliance", Description: "Net income → covered taxes → IIR top-up allocation (T9).", Triggerable: true},
		{ID: "wf-onb-tin-provision", Name: "Onboarding TIN provision", Plane: "inclusion", Description: "NIN=TIN / CAC-RC=TIN fusion via tin-graph (T5).", Triggerable: true},
		{ID: "wf-onb-capture-ingest", Name: "Capture ingest", Plane: "inclusion", Description: "Offline-first batch capture sync with idempotency keys (T5).", Triggerable: true},
		{ID: "wf-onb-commission-settlement", Name: "Commission settlement", Plane: "inclusion", Description: "Agent commission payout via ledger 700 (T5).", Triggerable: true},
		{ID: "wf-psm-payment", Name: "Presumptive payment", Plane: "inclusion", Description: "Intent → pending transfer → PSSP authorise → capture/void (T12).", Triggerable: true},
		{ID: "wf-psm-float-monitor", Name: "Agent float monitor", Plane: "inclusion", Description: "Float balance watch on ledger 100 accounts (T12).", Triggerable: true},
		{ID: "wf-psm-settlement", Name: "PSP settlement", Plane: "inclusion", Description: "Batch settlement with PSSP simulator (T12).", Triggerable: true},
		{ID: "wf-psm-gate-flip", Name: "PSM gate flip", Plane: "inclusion", Description: "Post-regulation gate activation ceremony (T12).", Triggerable: false},
		{ID: "wf-jrb-onboard", Name: "JRB authority onboard", Plane: "gov", Description: "mTLS+OIDC authority onboarding (T11).", Triggerable: true},
		{ID: "wf-jrb-eoi", Name: "JRB exchange of information", Plane: "gov", Description: "Four-party-visibility EOI lifecycle (T11).", Triggerable: true},
		{ID: "wf-jrb-attribution-publish", Name: "Attribution publish", Plane: "gov", Description: "NTAA 30% place-of-consumption attribution feed, signed (T11).", Triggerable: true},
		{ID: "wf-daily-scoring", Name: "Daily risk scoring", Plane: "gov", Description: "Transparent rule+score model with explanation payloads (T4).", Triggerable: true},
		{ID: "wf-entity-resolution", Name: "Entity resolution", Plane: "gov", Description: "Match entities using rp-identity-match-thresholds (T4).", Triggerable: true},
		{ID: "wf-case-evidence-pack", Name: "Case evidence pack", Plane: "gov", Description: "Assemble WORM evidence pack for Ombud case (T13i).", Triggerable: true},
	}
	s.WorkflowRuns = []WorkflowRun{
		{ID: "run-8812", WorkflowID: "wf-psm-settlement", Status: "completed", Triggered: "operator@meridian.local", Input: `{"batch":43}`, StartedAt: "2025-06-24T09:31:45Z", FinishedAt: "2025-06-24T09:32:20Z"},
		{ID: "run-8811", WorkflowID: "wf-daily-scoring", Status: "completed", Triggered: "cron", StartedAt: "2025-06-24T02:00:00Z", FinishedAt: "2025-06-24T02:11:07Z"},
		{ID: "run-8810", WorkflowID: "wf-jrb-attribution-publish", Status: "failed", Triggered: "jrb-svc", Input: `{"period":"2025-W25"}`, StartedAt: "2025-06-23T23:00:00Z", FinishedAt: "2025-06-23T23:00:41Z"},
	}
}

func seedSettings(s *Store) {
	s.Flags = map[string]bool{
		"pos.dual_shadow":            true,
		"etr.qdmtt_upgrade":          false,
		"analytics.pseudonymise_gold": true,
		"console.dev_seed_banner":     true,
		"ussd.simulator_callbacks":    true,
		"wht.small_company_carveout":  true,
	}
	s.APIKeys = []APIKey{
		{ID: "key-01", Name: "compliance-portal dev", Prefix: "mk_live_9f2c", Scopes: "read:packs read:health", CreatedAt: "2025-05-02T09:00:00Z", SecretTail: "…a91e"},
		{ID: "key-02", Name: "ci ceremony bot", Prefix: "mk_live_77b0", Scopes: "write:packs write:evidence", CreatedAt: "2025-05-14T09:00:00Z", SecretTail: "…04cd"},
	}
	s.NotifProviders = []NotifProvider{
		{Channel: "sms", Provider: "africas-talking-class adapter", Mode: "simulator", Status: "ok"},
		{Channel: "ussd", Provider: "aggregator callback webhook", Mode: "simulator", Status: "ok"},
		{Channel: "email", Provider: "smtp relay", Mode: "simulator", Status: "degraded"},
		{Channel: "push", Provider: "fcm-class adapter", Mode: "simulator", Status: "ok"},
	}
	s.Routes = []RouteRow{
		{Plane: "compliance", Path: "/einvoicing/*", Upstream: "einvoicing:8101", Methods: "GET,POST", Auth: "jwt"},
		{Plane: "compliance", Path: "/wht/*", Upstream: "wht:8103", Methods: "GET,POST", Auth: "jwt"},
		{Plane: "compliance", Path: "/pos/*", Upstream: "pos-vat:8105", Methods: "GET,POST", Auth: "jwt"},
		{Plane: "inclusion", Path: "/onboarding/*", Upstream: "onboarding:8201", Methods: "GET,POST", Auth: "jwt"},
		{Plane: "inclusion", Path: "/psm/*", Upstream: "presumptive:8202", Methods: "GET,POST", Auth: "jwt"},
		{Plane: "gov", Path: "/enclave/*", Upstream: "enclave-gateway:8304", Methods: "POST", Auth: "mtls+jwt"},
		{Plane: "core", Path: "/packs/*", Upstream: "rp-registry:8081", Methods: "GET,POST", Auth: "jwt"},
	}
}
