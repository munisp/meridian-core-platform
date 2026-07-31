# packages/events — the Meridian eventing spine

Canonical building blocks for every `nrs.*` topic family. See
`docs/ingestion.md` for the full architecture.

## Envelope (THE spec)

`envelope/` (Go) and `python/meridian_events/envelope.py` are the canonical
implementation. Every message on every `nrs.*` topic uses this shape:

```json
{"id": "<26-char ULID>", "type": "nrs.<family>.v1", "source": "svc-name",
 "time": "<RFC3339>", "tenant_id": "", "trace_id": "<32 hex>",
 "rule_pack_version": "", "data": { ... }}
```

## Schema registry (`schemareg/`)

- Go: `schemareg.NewDev()` loads the embedded dev store
  (`schemareg/schemas/*.json` + `schemareg/topics.json` — the topic catalog).
- Python: `meridian_events.schemareg.Registry()`.
- API: `Register / Lookup / Topics / ValidateData / ValidateEnvelope /
  CheckCompatibility` (BACKWARD compat: no new required fields, no type
  changes, no enum narrowing).
- Publish-time hook (Go): install once at service start —

  ```go
  reg, _ := schemareg.NewDev()
  bus.SetPublishValidator(reg.ValidateEnvelope)
  b := bus.NewFromEnv() // now validating
  ```

  `PROFILE=prod` → unregistered/invalid publishes are REJECTED; dev → warn
  and allow.

## Transactional outbox

Events must commit with the domain state — never `bus.Publish` after the
fact (a crash in between loses the event).

**Postgres services (Go):**

```go
pg := store.OpenPg(ctx, os.Getenv("DATABASE_URL")) // outbox table auto-created
err := pg.WithTx(ctx, func(tx pgx.Tx) error {
    if err := writeDomainTx(ctx, tx, op); err != nil { return err }
    env, _ := envelope.New("nrs.onb.provisioned.v1", "onboarding", "", "", payload)
    return store.AppendOutboxTx(ctx, tx, "nrs.onb.provisioned.v1", env)
})
go store.OutboxRelay(ctx, pg, b, store.OutboxRelayConfig{}) // FOR UPDATE SKIP LOCKED
```

Migration SQL: `store.PgOutboxDDL` (also applied by `store.OpenPg`).

**DuckDB services (Python, e.g. feature-store):**
`meridian_events.outbox.DuckDBOutbox` — append inside the same transaction
as the domain writes, drain with `OutboxRelay` (see
`services/feature-store/app/main.py`).

**Dev / embedded services:** `outbox.FileStore` (Go) / `FileOutbox` (Python)
JSONL outbox + relay, as today.

**Reference implementations:** `services/search-indexer` (consumer side:
claims `meridian_outbox` rows via FOR UPDATE SKIP LOCKED when `OUTBOX_PG=1`)
and `services/feature-store` (producer side: same-tx DuckDB outbox +
relay). Copy either pattern for the remaining services (see the checklist
in `docs/ingestion.md`).

## Legacy producers

Consumers must upgrade non-enveloped raw maps at the edge:
`meridian_events.shim.coerce_envelope(topic, msg, source_hint=...)` — wraps
legacy maps (e.g. ussd-gateway's `nrs.onb.ussd.v1` raw maps) into the
canonical envelope, tagged (`data.upgraded_legacy=true`,
`source="…(legacy-shim)"`), with pseudonymised MSISDN/NIN and a
deterministic id for dedup. The lakehouse sink does this automatically.
