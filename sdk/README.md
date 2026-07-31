# Meridian SDKs

Hand-written, generate-free typed SDKs for the Meridian platform. The source
of truth is the OpenAPI 3.1 catalogue in [`../api/`](../api/) (8 services:
onboarding, tin-graph, ledger, rules-engine, settlement, rp-registry,
consent, kyc-engine placeholder). Typed structs/models here are reviewed
against those specs — no codegen pipeline, no drift.

Coverage (highest-value services first): **onboarding, tin-graph, ledger**.
Both SDKs support `Idempotency-Key` on every mutating call
(`NewIdempotencyKey()` / `new_idempotency_key()`).

## Go (`sdk/go`)

```go
import meridian "github.com/munisp/meridian-core-platform/sdk/go"

c := meridian.NewClient("http://localhost:8101") // dev profile (X-Dev-Role)
ctx := context.Background()

// register + verify + provision through the durable workflow
op, _ := c.Onboarding().CreateOperator(ctx, meridian.OperatorCreate{
    NIN: "12345678901", FullName: "Adaeze Okafor", AgentID: "ag_7",
}, meridian.NewIdempotencyKey())
run, _ := c.Onboarding().ProvisionTIN(ctx, op.ID, "12345678901")
if run.Status == "failed" {                       // e.g. NIMC outage
    run, _ = c.Onboarding().RedriveRun(ctx, run.ID) // idempotent re-drive
}

// resumption: where is this onboarding, what is missing?
st, _ := c.Onboarding().Resumption(ctx, op.ID)
fmt.Println(st.CurrentStep, st.MissingItems)

// business KYB via tin-graph (directors/UBOs, >25% derived server-side)
tg := meridian.NewClient("http://localhost:8003")
prov, _ := tg.TinGraph().ProvisionTIN(ctx, meridian.ProvisionRequest{
    CACRC: "RC123456",
    Company: &meridian.CompanyProfile{
        CompanyName: "Test Ltd", RCNumber: "RC123456",
        Shareholders: []meridian.Shareholder{
            {PersonRef: meridian.PersonRef{Name: "A Bello"}, SharePercent: 40},
        },
    },
})
fmt.Println(prov.UBOs) // derived UBOs (>25%)

// ledger (kobo)
lg := meridian.NewClient("http://localhost:8002")
bal, _ := lg.Ledger().Balance(ctx, "700000000001-1")
```

Errors are typed: `*meridian.Problem{Status, Title, Detail}` (e.g. 409
`illegal_transition` from the lifecycle state machine).

## Python (`sdk/python`)

```bash
pip install ./sdk/python   # httpx + pydantic v2
```

```python
from meridian_sdk import Client, OperatorCreate, new_idempotency_key

ob = Client("http://localhost:8101").onboarding()
op = ob.create_operator(OperatorCreate(nin="12345678901", full_name="Adaeze Okafor",
                                       agent_id="ag_7"),
                        idempotency_key=new_idempotency_key())
run = ob.provision_tin(op.id, "12345678901")
if run.status == "failed":
    run = ob.redrive_run(run.id)

tg = Client("http://localhost:8003").tingraph()
res = tg.provision_tin(cac_rc="RC123456")
ubos = tg.entity_ubos(res.entity.id).ubos

lg = Client("http://localhost:8002").ledger()
print(lg.balance("700000000001-1").available)
```

Errors raise `meridian_sdk.MeridianError(status, title, detail)`.

## Tests

- Go: `cd sdk/go && go test ./...` (httptest stub of all three services)
- Python: `cd sdk/python && PYTHONPATH=. pytest` (httpx MockTransport)
