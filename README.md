# meridian-core-platform

**Meridian TaxTech: shared platform core — middleware spine, rule-pack registry, TIN graph, feature store, TigerBeetle ledger services, edge config (monorepo).**

## Purpose
The shared platform layer consumed by every Meridian suite. Hosts cross-cutting services (identity/TIN graph, ledger, rule-pack consumption, feature store) and the edge/runtime config plane. No suite ships business logic that belongs here.

## Plane mapping
- **Control plane:** rule-pack registry consumer, edge config distribution
- **Data plane:** TIN graph, feature store, TigerBeetle ledger services
- **Execution plane:** middleware spine (workflows-go, geo-rs)

## Monorepo layout (unified doc §8.2)
| Path | Contents |
|------|----------|
| `services/` | Long-running Go services (TIN graph, feature store, ledger adapters) |
| `packages/` | Shared libraries/SDKs consumed by the suites |
| `infra/` | IaC, deployment manifests, environment config |
| `workflows-go/` | Durable workflow engine & workflow definitions (Go) |
| `geo-rs/` | Geospatial / geofencing services (Rust) |
| `rule-packs/consumer/` | Signed rule-pack verification & consumption client |
| `api/` | Public API contracts (OpenAPI/Protobuf) |
| `cmd/` | Service entrypoints / CLI binaries |

## Sibling repositories
- [meridian-compliance-suite](https://github.com/munisp/meridian-compliance-suite)
- [meridian-inclusion-suite](https://github.com/munisp/meridian-inclusion-suite)
- [meridian-gov-enclave](https://github.com/munisp/meridian-gov-enclave)
- [meridian-rule-packs](https://github.com/munisp/meridian-rule-packs)
- [meridian-docs](https://github.com/munisp/meridian-docs)

**Status:** scaffold
