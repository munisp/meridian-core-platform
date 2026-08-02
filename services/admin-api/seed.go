package main

// seed.go — service registry (15 core services + plane apps), rule-pack seed
// summaries, gate seeds and the F1–F10 cross-zone flow matrix.

func seedServices(s *Store) {
	add := func(e ServiceEntry) {
		if e.HealthPath == "" {
			e.HealthPath = "/healthz"
		}
		if e.Version == "" {
			e.Version = "0.1.0"
		}
		e.Enabled = true
		e.HealthStatus = "unknown"
		s.Services[e.ID] = &e
	}

	// ---- 15 core services (SPEC §2 layout, incl. geo-rs) ----
	add(ServiceEntry{ID: "rp-registry", Name: "Rule-Pack Registry", Plane: "core", Kind: "service", TItems: []string{"rp-*"}, BaseURL: "http://localhost:8002", URLEnv: "RP_REGISTRY_URL", Description: "Registers, signs and serves rp-* rule-pack versions."})
	add(ServiceEntry{ID: "tin-graph", Name: "TIN Graph / Identity", Plane: "core", Kind: "service", TItems: []string{"T5", "T11"}, BaseURL: "http://localhost:8003", URLEnv: "TIN_GRAPH_URL", Description: "NIN=TIN / CAC-RC=TIN fusion, verification adapters, entity resolution."})
	add(ServiceEntry{ID: "rules-engine", Name: "Rules Engine", Plane: "core", Kind: "service", TItems: []string{"rp-*"}, BaseURL: "http://localhost:8001", URLEnv: "RULES_ENGINE_URL", Description: "Generic rp-* evaluation API (FastAPI)."})
	add(ServiceEntry{ID: "feature-store", Name: "Feature Store", Plane: "core", Kind: "service", TItems: []string{"T4"}, BaseURL: "http://localhost:8012", URLEnv: "FEATURE_STORE_URL", Description: "Offline/online feature materialisation."})
	add(ServiceEntry{ID: "ledger", Name: "Ledger (TigerBeetle)", Plane: "core", Kind: "service", TItems: []string{"T12"}, BaseURL: "http://localhost:8010", URLEnv: "LEDGER_URL", Description: "Double-entry ledger; dev in-memory TigerBeetle semantics."})
	add(ServiceEntry{ID: "notification", Name: "Notification", Plane: "core", Kind: "service", TItems: []string{"T5", "T12", "T14"}, BaseURL: "http://localhost:8006", URLEnv: "NOTIFICATION_URL", Description: "SMS/USSD/email/push via provider interfaces + simulators."})
	add(ServiceEntry{ID: "audit-evidence", Name: "Audit & Evidence (WORM)", Plane: "core", Kind: "service", TItems: []string{"all"}, BaseURL: "http://localhost:8004", URLEnv: "AUDIT_EVIDENCE_URL", Description: "Append-only audit log, WORM evidence store, TAT assembly."})
	add(ServiceEntry{ID: "geo", Name: "Geo Attribution", Plane: "core", Kind: "service", TItems: []string{"T6", "T11"}, BaseURL: "http://localhost:8005", URLEnv: "GEO_URL", Description: "Point→state/LGA/ward attribution (calls geo-rs)."})
	add(ServiceEntry{ID: "geo-rs", Name: "geo-rs (Rust)", Plane: "core", Kind: "service", TItems: []string{"T6", "T11"}, BaseURL: "http://localhost:8100", URLEnv: "GEO_RS_URL", Description: "Point-in-polygon + haversine prefilter engine."})
	add(ServiceEntry{ID: "consent", Name: "Consent (NDPA)", Plane: "core", Kind: "service", TItems: []string{"T5", "T14"}, BaseURL: "http://localhost:8007", URLEnv: "CONSENT_URL", Description: "NDPA consent capture, revocation and receipts."})
	add(ServiceEntry{ID: "reg-watch", Name: "Reg-Watch / Gates", Plane: "core", Kind: "service", TItems: []string{"G1", "G2", "G8"}, BaseURL: "http://localhost:8011", URLEnv: "REG_WATCH_URL", Description: "Gate & gazette monitor; board-authorised gate flips."})
	add(ServiceEntry{ID: "search-indexer", Name: "Search Indexer", Plane: "core", Kind: "service", TItems: []string{"all"}, BaseURL: "http://localhost:8008", URLEnv: "SEARCH_INDEXER_URL", Description: "Outbox→OpenSearch indexer (dev: local JSON index)."})
	add(ServiceEntry{ID: "settlement", Name: "Settlement / Recon", Plane: "core", Kind: "service", TItems: []string{"T12"}, BaseURL: "http://localhost:8013", URLEnv: "SETTLEMENT_URL", Description: "PSSP 3-way reconciliation (platform vs PSSP vs treasury)."})
	add(ServiceEntry{ID: "edge-policy", Name: "Edge Policy (APISIX)", Plane: "core", Kind: "service", TItems: []string{"all"}, BaseURL: "http://localhost:8009", URLEnv: "EDGE_POLICY_URL", Description: "Route policy distribution + WAF mode control."})
	add(ServiceEntry{ID: "admin-api", Name: "Admin API (this service)", Plane: "core", Kind: "service", TItems: []string{"console"}, BaseURL: "http://localhost:8095", URLEnv: "ADMIN_API_URL", Description: "Management-plane aggregation, auth and proxy backend."})

	// ---- compliance plane (Market Zone) ----
	add(ServiceEntry{ID: "einvoicing", Name: "E-Invoicing (MBS)", Plane: "compliance", Kind: "service", TItems: []string{"T1", "T2"}, BaseURL: "http://localhost:8110", URLEnv: "EINVOICING_URL", Description: "UBL 2.1/Peppol BIS mapping, CSID signing, pre-clearance."})
	add(ServiceEntry{ID: "rev360", Name: "Rev360 Reconciliation", Plane: "compliance", Kind: "service", TItems: []string{"T3"}, BaseURL: "http://localhost:8120", URLEnv: "REV360_URL", Description: "Reconciliation workbench + defect-class rules engine."})
	add(ServiceEntry{ID: "wht", Name: "WHT 2024", Plane: "compliance", Kind: "service", TItems: []string{"T7"}, BaseURL: "http://localhost:8130", URLEnv: "WHT_URL", Description: "Withholding tax evaluation via rp-wht-2024, credit ledger."})
	add(ServiceEntry{ID: "tp-cbcr", Name: "TP / CbCR", Plane: "compliance", Kind: "service", TItems: []string{"T8"}, BaseURL: "http://localhost:8140", URLEnv: "TP_CBCR_URL", Description: "OECD CbCR XML, master/local file assembly."})
	add(ServiceEntry{ID: "pos-vat", Name: "POS VAT", Plane: "compliance", Kind: "service", TItems: []string{"T6"}, BaseURL: "http://localhost:8106", URLEnv: "POS_VAT_URL", Description: "POS receipt ingest, attribution, offline spool."})
	add(ServiceEntry{ID: "etr", Name: "ETR / GloBE", Plane: "compliance", Kind: "service", TItems: []string{"T9"}, BaseURL: "http://localhost:8109", URLEnv: "ETR_URL", Description: "ETR compute engine, GIR/filing-pack builder."})
	add(ServiceEntry{ID: "vasp-carf", Name: "VASP / CARF", Plane: "compliance", Kind: "service", TItems: []string{"T10"}, BaseURL: "http://localhost:8116", URLEnv: "VASP_CARF_URL", Description: "VASP trade ingest, cost-basis, CARF XML builder."})
	add(ServiceEntry{ID: "case-mgmt", Name: "Practitioner Case Mgmt", Plane: "compliance", Kind: "service", TItems: []string{"T13p"}, BaseURL: "http://localhost:8113", URLEnv: "CASE_MGMT_URL", Description: "Matters, documents, deadlines, evidence packs."})
	add(ServiceEntry{ID: "compliance-portal", Name: "Compliance Portal", Plane: "compliance", Kind: "app", TItems: []string{"T1", "T6", "T7", "T9", "T10", "T13p"}, BaseURL: "http://localhost:5174", URLEnv: "COMPLIANCE_PORTAL_URL", HealthPath: "/", Description: "Taxpayer/practitioner/retailer portal."})

	// ---- inclusion plane (Market Zone) ----
	add(ServiceEntry{ID: "onboarding", Name: "Onboarding", Plane: "inclusion", Kind: "service", TItems: []string{"T5"}, BaseURL: "http://localhost:8101", URLEnv: "ONBOARDING_URL", Description: "wf-onb-*, NIMC adapter, offline capture sync."})
	add(ServiceEntry{ID: "presumptive", Name: "Presumptive Tax (PSM)", Plane: "inclusion", Kind: "service", TItems: []string{"T12"}, BaseURL: "http://localhost:8102", URLEnv: "PRESUMPTIVE_URL", Description: "Payment intents, PSSP adapter, certificates, agent float."})
	add(ServiceEntry{ID: "education", Name: "Tax Education", Plane: "inclusion", Kind: "service", TItems: []string{"T14"}, BaseURL: "http://localhost:8103", URLEnv: "EDUCATION_URL", Description: "Calculators with citations, FAQ corpus, embed SDK."})
	add(ServiceEntry{ID: "ussd-gateway", Name: "USSD Gateway", Plane: "inclusion", Kind: "service", TItems: []string{"T5", "T12"}, BaseURL: "http://localhost:8104", URLEnv: "USSD_GATEWAY_URL", Description: "Menu-graph session engine, aggregator webhook simulator."})
	add(ServiceEntry{ID: "agent-pwa", Name: "Agent PWA", Plane: "inclusion", Kind: "app", TItems: []string{"T5"}, BaseURL: "http://localhost:5201", URLEnv: "AGENT_PWA_URL", HealthPath: "/", Description: "Offline-first agent capture app."})
	add(ServiceEntry{ID: "operator-pwa", Name: "Operator PWA", Plane: "inclusion", Kind: "app", TItems: []string{"T12", "T14"}, BaseURL: "http://localhost:5202", URLEnv: "OPERATOR_PWA_URL", HealthPath: "/", Description: "Operator self-service app."})

	// ---- gov plane (Sovereign Zone) ----
	add(ServiceEntry{ID: "analytics", Name: "Analytics / Scoring", Plane: "gov", Kind: "service", TItems: []string{"T4", "T15"}, BaseURL: "http://localhost:8401", URLEnv: "ANALYTICS_URL", Description: "Lakehouse-lite, daily scoring, NSW ingest."})
	add(ServiceEntry{ID: "jrb", Name: "JRB Service", Plane: "gov", Kind: "service", TItems: []string{"T11"}, BaseURL: "http://localhost:8402", URLEnv: "JRB_URL", Description: "Authority registry, EOI, attribution feeds."})
	add(ServiceEntry{ID: "ombud", Name: "Tax Ombud", Plane: "gov", Kind: "service", TItems: []string{"T13i"}, BaseURL: "http://localhost:8403", URLEnv: "OMBUD_URL", Description: "Case registry, deposit tracker, evidence packs."})
	add(ServiceEntry{ID: "enclave-gateway", Name: "Enclave Gateway", Plane: "gov", Kind: "service", TItems: []string{"F1-F8"}, BaseURL: "http://localhost:8400", URLEnv: "ENCLAVE_GATEWAY_URL", Description: "Audited API gateway — sole north-south path; WORM receipts."})
	add(ServiceEntry{ID: "gov-console", Name: "Gov Console", Plane: "gov", Kind: "console", TItems: []string{"T4", "T11", "T13i"}, BaseURL: "http://localhost:8404", URLEnv: "GOV_CONSOLE_URL", HealthPath: "/", Description: "NRS/JRB/state-IRS/Ombud console."})
}

func seedPacksGatesFlows(s *Store) {
	s.Gates["G1"] = &Gate{ID: "G1", Name: "CTCs confirmed (gazette)", Description: "WHT 2024 regulations confirmed by gazette; packs carry subject_to_regazette until open.", State: false, UpdatedAt: "2025-06-24T08:20:03Z", Source: "dev-seed"}
	s.Gates["G2"] = &Gate{ID: "G2", Name: "Rivers case resolved", Description: "VAT attribution litigation gate; dual_shadow mode stays on until resolved.", State: false, UpdatedAt: "2025-06-01T00:00:00Z", Source: "dev-seed"}
	s.Gates["G8"] = &Gate{ID: "G8", Name: "Presumptive regulation gazetted", Description: "Post-regulation gate for presumptive payment enforcement.", State: false, UpdatedAt: "2025-06-01T00:00:00Z", Source: "dev-seed"}
	s.Gates["carf.transmit_enabled"] = &Gate{ID: "carf.transmit_enabled", Name: "CARF transmit", Description: "Enables outbound CARF XML transmission to exchange partners.", State: false, UpdatedAt: "2025-06-24T08:20:03Z", Source: "dev-seed"}
	s.Gates["qdmtt_upgrade"] = &Gate{ID: "qdmtt_upgrade", Name: "QDMTT upgrade", Description: "Switches ETR engine to qualified domestic minimum top-up tax basis.", State: false, UpdatedAt: "2025-06-01T00:00:00Z", Source: "dev-seed"}
	s.Gates["ombud.rules_active"] = &Gate{ID: "ombud.rules_active", Name: "Ombud rules active", Description: "Activation gate on Ombud procedure packs (rp-procedure-ombud).", State: true, UpdatedAt: "2025-06-10T12:00:00Z", Source: "dev-seed"}

	s.Receipts = []FlowReceipt{
		{ID: "rcpt-0091", Flow: "F1", Correlation: "irn:IRN-2025-000912", Sender: "einvoicing", WORMURI: "worm://minio/evidence/2025/06/24/ev-ubl-receipt-0091.json", SHA256: "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae", Status: "accepted", Detail: "UBL pre-clearance receipt sealed before enclave consumer delivery", Timestamp: "2025-06-24T09:07:31Z"},
		{ID: "rcpt-0092", Flow: "F2", Correlation: "b2c:batch-2025-W26-04", Sender: "einvoicing", WORMURI: "worm://minio/evidence/2025/06/24/b2c-04.json", SHA256: "fcde2b2edba56bf408601fb721fe9b5c338d10ee429ea04fae5511b68fbf8fb9", Status: "accepted", Detail: "B2C real-time report batch (1,204 receipts)", Timestamp: "2025-06-24T09:40:12Z"},
		{ID: "rcpt-0093", Flow: "F7", Correlation: "attr:2025-W25", Sender: "jrb", WORMURI: "worm://minio/evidence/2025/06/23/attr-w25.json", SHA256: "2a92d4b7bfc02c0d6e26ee8f36b97ba1f36502b3791698f91dbd69d8d1c2e21f", Status: "accepted", Detail: "Attribution feed served to 12 state authorities (signed)", Timestamp: "2025-06-23T23:05:00Z"},
		{ID: "rcpt-0094", Flow: "F5", Correlation: "psm:batch-42", Sender: "presumptive", WORMURI: "worm://minio/evidence/2025/06/24/psm-42.json", SHA256: "b5bb9d8014a0f9b1d61e21e796d78dccdf1352f23cd32812f4850b878ae4944c", Status: "accepted", Detail: "Presumptive remittance batch 42 (₦90.4m)", Timestamp: "2025-06-24T09:04:40Z"},
		{ID: "rcpt-0095", Flow: "F3", Correlation: "carf:msg-2025-061", Sender: "vasp-carf", WORMURI: "worm://minio/evidence/2025/06/22/carf-061.json", SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", Status: "rejected", Detail: "carf.transmit_enabled gate closed — message quarantined", Timestamp: "2025-06-22T14:11:00Z"},
	}
	// s.Forbidden intentionally left empty: F9/F10 are forbidden by construction.
}

// FlowMatrix is static (from the architecture doc) — not mutable at runtime.
func flowMatrix() []FlowDef {
	return []FlowDef{
		{ID: "F1", Name: "E-invoice pre-clearance", Direction: "market→sovereign", Payload: "UBL 2.1 invoices with IRN + crypto stamp", Topics: "nrs.mbs.preclearance.v1", Allowed: true, Note: "Received by enclave-gateway; schema-validated vs rp-ubl-bis; WORM receipt before delivery."},
		{ID: "F2", Name: "B2C real-time reports", Direction: "market→sovereign", Payload: "Batched consumer receipts", Topics: "nrs.mbs.b2c.v1", Allowed: true, Note: "Near-real-time; pseudonymised at gateway."},
		{ID: "F3", Name: "CARF messages", Direction: "market→sovereign", Payload: "OECD CARF XML reports", Topics: "nrs.vasp.carf.v1", Allowed: true, Note: "Gated by carf.transmit_enabled."},
		{ID: "F4", Name: "ETR filings", Direction: "market→sovereign", Payload: "GIR / filing packs", Topics: "nrs.globe.filing.v1", Allowed: true, Note: "Validated vs rp-gir-schema."},
		{ID: "F5", Name: "Presumptive remittances", Direction: "market→sovereign", Payload: "PSM settlement batches", Topics: "nrs.psm.remittance.v1", Allowed: true, Note: "Tied to ledger 200 transfers."},
		{ID: "F6", Name: "Onboarding registrations", Direction: "market→sovereign", Payload: "TIN-provision confirmations (tin_hash only)", Topics: "nrs.onb.provisioned.v1", Allowed: true, Note: "Pseudonymised — no raw NIN/TIN crosses zones."},
		{ID: "F7", Name: "Attribution feeds", Direction: "sovereign→market", Payload: "Signed NTAA 30% place-of-consumption feeds", Topics: "nrs.jrb.attribution.v1", Allowed: true, Note: "Served by JRB via gateway; signed output."},
		{ID: "F8", Name: "WHT reconciliation", Direction: "sovereign→market", Payload: "WHT credit recon results", Topics: "nrs.wht.recon.v1", Allowed: true, Note: "Served to compliance plane."},
		{ID: "F9", Name: "Raw taxpayer data push", Direction: "sovereign→market", Payload: "Any non-pseudonymised taxpayer record", Topics: "—", Allowed: false, Note: "FORBIDDEN by construction: no code path exists; gateway middleware denies."},
		{ID: "F10", Name: "Direct zone bypass", Direction: "any", Payload: "Service-to-service calls bypassing the audited gateway", Topics: "—", Allowed: false, Note: "FORBIDDEN by construction: network policy + middleware deny."},
	}
}

// PackSeeds are the dev-seed pack summaries used when rp-registry is unreachable.
func packSeeds() []PackSummary {
	return []PackSummary{
		{ID: "rp-wht-2024", LatestVersion: "1.0.0", Status: "published", EffectiveFrom: "2025-01-01", Signed: true, SourceCitation: "Deduction of Tax at Source (WHT) Regulations 2024", SubjectToRegazett: true, StaleConsumers: 1},
		{ID: "rp-ubl-bis", LatestVersion: "1.0.0", Status: "published", EffectiveFrom: "2025-01-01", Signed: true, SourceCitation: "NRS e-invoicing implementation guideline (UBL 2.1 / Peppol BIS)", SubjectToRegazett: true},
		{ID: "rp-mbs-business-rules", LatestVersion: "1.0.0", Status: "published", EffectiveFrom: "2025-01-01", Signed: true, SourceCitation: "MBS pre-clearance business rules", SubjectToRegazett: true},
		{ID: "rp-vat-rates", LatestVersion: "1.0.0", Status: "published", EffectiveFrom: "2025-01-01", Signed: true, SourceCitation: "VAT Act as amended (7.5%)", SubjectToRegazett: true},
		{ID: "rp-vat-attribution-mode", LatestVersion: "1.0.0", Status: "published", EffectiveFrom: "2025-01-01", Signed: true, SourceCitation: "Federal/state VAT attribution switch (G2 dual_shadow)", SubjectToRegazett: true},
		{ID: "rp-presumptive-federal", LatestVersion: "1.0.0", Status: "published", EffectiveFrom: "2025-01-01", Signed: true, SourceCitation: "Presumptive tax regime (federal baseline)", SubjectToRegazett: true, StaleConsumers: 2},
		{ID: "rp-presumptive-lagos", LatestVersion: "1.0.0", Status: "published", EffectiveFrom: "2025-01-01", Signed: true, SourceCitation: "Lagos presumptive bands", SubjectToRegazett: true},
		{ID: "rp-turnover-bands", LatestVersion: "1.0.0", Status: "published", EffectiveFrom: "2025-01-01", Signed: true, SourceCitation: "Turnover band table (≤₦25m micro … >₦100m)", SubjectToRegazett: true},
		{ID: "rp-etr-nta", LatestVersion: "1.0.0", Status: "review", EffectiveFrom: "2026-01-01", Signed: false, SourceCitation: "Nigeria Tax Act ETR provisions", SubjectToRegazett: true},
		{ID: "rp-globe-oecd", LatestVersion: "1.0.0", Status: "simulation", EffectiveFrom: "2026-01-01", Signed: false, SourceCitation: "OECD GloBE model rules", SubjectToRegazett: true},
		{ID: "rp-carf-schema", LatestVersion: "1.0.0", Status: "review", EffectiveFrom: "2026-01-01", Signed: false, SourceCitation: "OECD CARF XML schema", SubjectToRegazett: true},
		{ID: "rp-education-ng", LatestVersion: "1.0.0", Status: "published", EffectiveFrom: "2025-01-01", Signed: true, SourceCitation: "Effective-dated rate tables (PIT/CIT/VAT/rent relief)", SubjectToRegazett: true},
		{ID: "rp-identity-match-thresholds", LatestVersion: "1.0.0", Status: "published", EffectiveFrom: "2025-01-01", Signed: true, SourceCitation: "Entity resolution match thresholds", SubjectToRegazett: true},
		{ID: "rp-attribution-formula", LatestVersion: "1.0.0", Status: "published", EffectiveFrom: "2025-01-01", Signed: true, SourceCitation: "NTAA 30% place-of-consumption formula", SubjectToRegazett: true},
		{ID: "rp-disclosure-control", LatestVersion: "1.0.0", Status: "published", EffectiveFrom: "2025-01-01", Signed: true, SourceCitation: "k-anonymity disclosure control on aggregates", SubjectToRegazett: true},
		{ID: "rp-procedure-ombud", LatestVersion: "1.0.0", Status: "published", EffectiveFrom: "2025-01-01", Signed: true, SourceCitation: "Tax Ombud procedure rules", SubjectToRegazett: true},
	}
}
