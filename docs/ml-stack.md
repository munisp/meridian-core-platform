# ML Stack — Component Value Analysis & Integration Plan

Evaluation of five candidate components against the Meridian tax platform's
ML stack (see `ml/README.md`). Recommendation summary first, details below.

| Component | What it is | Adds to Meridian | Priority |
|---|---|---|---|
| **FalkorDB** | GraphBLAS-backed graph DB | Fast graph feature store for the GNN + KGQA substrate | **P1 — adopt** |
| **ollama** | Local LLM serving | Sovereign in-enclave LLM: KGQA + analyst copilot, zero data egress | **P1 — adopt** |
| **CocoIndex** | Data transformation/indexing framework | Feature + embedding pipelines from platform DBs | **P2 — adopt after P1** |
| **EPR-KGQA** | Entity-path-reasoning KG QA | Natural-language analyst queries with cited graph paths | **P2 — adopt with FalkorDB+ollama** |
| **ART** | Adversarial Robustness Toolbox | Evasion testing/hardening of the fraud models | **P3 — adopt in CI** |

Data-sovereignty constraint: taxpayer data must not leave the enclave. All
five components are self-hostable; ollama is specifically valued because it
eliminates LLM data egress entirely.

---

## 1. CocoIndex — data transformation & indexing framework

**What it adds.** Declarative, incrementally-updated pipelines that extract
from source systems (Postgres, Kafka, object stores), transform, and index
into vector/graph/search targets. Exactly the shape of our
`ml/data` → feature-store problem.

**Where it slots.** Replaces hand-rolled sync between platform Postgres and
the ML feature store/lakehouse: declare flows for transactions, filings, and
entity graphs; CocoIndex keeps parquet features *and* embedding indexes
(for KGQA retrieval) fresh as events land. Complements — does not replace —
the `ml/data` generator/extractor, which owns Nigerian-pattern semantics.

**Priority: P2.** Real value once production data flows; until then the
synthetic generator dominates.

## 2. EPR-KGQA — entity-path-reasoning KG question answering

**What it adds.** Natural-language questions answered by reasoning over
*paths* in a knowledge graph, returning the entity path as evidence — e.g.
"Which agents connect taxpayer T-… to the structuring cluster?" answered
with a cited TIN→transaction→agent→account path rather than a bare string.

**Where it slots.** Analyst/auditor interface over the TIN knowledge graph
stored in FalkorDB (§3), with the LLM served by ollama (§4). This is the
compliance-portal copilot: answers carry graph-path citations that auditors
can verify, matching Meridian's evidence-first posture (WORM audit store).

**KGQA flow:** `ollama (LLM, in-enclave) → EPR path reasoning over FalkorDB → cited answers for auditors`.
No tokens leave the enclave; every answer is traceable to graph edges.

**Priority: P2** (depends on FalkorDB + ollama).

## 3. FalkorDB — GraphBLAS graph database

**What it adds.** Redis-lineage graph DB executing Cypher over sparse linear
algebra (GraphBLAS) — very fast multi-hop traversals and graph aggregations
on commodity CPU, which is precisely our hardware profile.

**Where it slots.** Dual role:
1. **Graph feature store for the GNN** (`ml/models/gnn_gcn.py`): community
   structure, degree/pagerank-style features, and collusion-ring
   neighbourhoods materialised from FalkorDB into training/serving features
   instead of ad-hoc Postgres recursive CTEs.
2. **KGQA substrate**: the TIN/entity knowledge graph EPR-KGQA reasons over.

**Priority: P1.** Cheapest-to-adopt component with the broadest payoff
(GNN features + KGQA + analyst graph queries share one store).

## 4. ollama — local LLM serving

**What it adds.** One-command serving of open-weight models (Llama, Qwen,
Mistral) on CPU/GPU behind an OpenAI-compatible HTTP API, fully on-prem.

**Where it slots.** The enclave LLM runtime for (a) EPR-KGQA question
decomposition/answer synthesis and (b) the analyst copilot in the compliance
portal (case summarisation, rule-pack explanations). OpenAI-compatible API
means the services code against a stable interface and the model can be
swapped without code changes. **No data egress** — decisive for taxpayer
data sovereignty.

**Priority: P1** (with FalkorDB).

## 5. ART — Adversarial Robustness Toolbox

**What it adds.** Standard library for evasion attacks (FGSM/PGD/CW-style),
defences (adversarial training, input preprocessing), and robustness metrics
across frameworks including PyTorch.

**Where it slots.** Offline/CI hardening of the fraud models
(`ml/models/fraud_mlp.py`, `fraudfusion.py`): adversaries *will* probe the
fraud scorer (structuring amounts just under thresholds is already an
evasion pattern). Add an ART evaluation step to `ml/training/evaluate.py`
and a CI gate: report robust accuracy under bounded perturbation of
continuous features (amount, hour, counts) and fail on regression.

**Priority: P3** — high value, low urgency; no production surface change.

---

## compose.prod additions

Add to `infra/docker-compose.prod.yml`:

```yaml
  falkordb:
    image: falkordb/falkordb:4.2
    restart: unless-stopped
    ports: ["127.0.0.1:6380:6379"]   # enclave-internal only
    volumes:
      - falkordb-data:/data
    healthcheck:
      test: ["CMD", "redis-cli", "PING"]
      interval: 10s
      timeout: 3s
      retries: 5
    deploy:
      resources:
        limits: {memory: 4G}

  ollama:
    image: ollama/ollama:0.5
    restart: unless-stopped
    ports: ["127.0.0.1:11434:11434"]  # enclave-internal only, no egress
    volumes:
      - ollama-models:/root/.ollama
    healthcheck:
      test: ["CMD", "ollama", "list"]
      interval: 15s
      timeout: 5s
      retries: 5
    deploy:
      resources:
        limits: {memory: 8G}          # CPU-only quantized 7B fits; raise w/ GPU

  # KGQA service (built from this repo) wiring the two together:
  #   kgqa:
  #     environment:
  #       OLLAMA_URL: http://ollama:11434
  #       FALKORDB_URL: redis://falkordb:6379
  #     depends_on: [falkordb, ollama]

volumes:
  falkordb-data:
  ollama-models:
```

**KGQA request flow (auditor-cited answers):**
analyst question → `kgqa` service → ollama (in-enclave LLM) decomposes the
question → EPR entity-path reasoning executes traversals over FalkorDB →
ollama synthesises the answer **with the graph path attached as citation** →
compliance portal renders answer + auditable path evidence. No tokens or
taxpayer data leave the enclave at any step.

## Adoption sequence

1. **P1**: FalkorDB + ollama services in compose; wire GNN feature
   extraction to FalkorDB; stand up ollama behind the enclave gateway.
2. **P2**: EPR-KGQA service (ollama→FalkorDB cited answers); CocoIndex flows
   replacing hand-rolled feature sync once production data flows.
3. **P3**: ART robustness evaluation in `ml/training/evaluate.py` + CI gate
   in `ci/workflows/ml.yml`.
