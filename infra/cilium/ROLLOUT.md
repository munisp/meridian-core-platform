# Cilium zero-downtime rollout (SPEC C section 6)

Prereqs: kernel >= 5.10 on all nodes; `cilium` CLI installed; existing mTLS
kept in place (Cilium/WireGuard is defense in depth, not a replacement).

## Phase 1 — Install in observe mode (week 1)

Install with `policyEnforcementMode: "never"` (see `cilium-values.yaml`) and
Hubble on. Observe actual flows for one week and generate the allow-list from
real traffic:

```sh
helm install cilium cilium/cilium -n kube-system -f infra/cilium/cilium-values.yaml
hubble observe --since 24h --output json > flows-baseline.json
```

Validation: `cilium status`, `cilium connectivity test`.

## Phase 2 — Apply CNPs in audit mode

Apply every policy in `infra/cilium/policies/` (default-deny + explicit
allows for meridian-core, inclusion, enclave, data, kafka, platform-auth,
monitoring). With `policyEnforcementMode: "never"` they are audit-only;
check drops that *would* have occurred:

```sh
kubectl apply -f infra/cilium/policies/
hubble observe --verdict DROPPED --since 1h
```

Fix policy gaps until would-be drops are only truly-unexpected traffic.

## Phase 3 — Flip enforcement per namespace (in this order)

`inclusion` → `meridian-core` → `data` → `enclave`. Use
`policyEnforcementMode: "default"` plus per-namespace enablement
(`cilium policy` / namespace annotations), or staged helm upgrades. After
each namespace:

```sh
cilium connectivity test --include-unsafe-tests
kubectl get cep -n <ns>
```

## Phase 4 — WireGuard encryption

Enable `encryption.enabled=true, type=wireguard, nodeEncryption=true` during
a rolling node reboot window. Validate:

```sh
cilium encrypt status
kubectl -n kube-system exec ds/cilium -c cilium -- cilium-dbg encrypt status
```

## Phase 5 — Tetragon

Deploy `infra/cilium/tetragon-policies.yaml` in **monitor mode** (Post only)
for 2 weeks; then enable Sigkill enforcement for WORM writes only
(`enclave-worm-guard` write selector). `enclave-exec-trace` stays Post-only
until phase 2 of enforcement.

```sh
kubectl apply -f infra/cilium/tetragon-policies.yaml
kubectl -n kube-system exec ds/tetragon -c export-stdout -- \
  tetra getevents -o compact --namespace enclave
```

## Validation gate (all must pass)

- `cilium connectivity test` — green.
- **Chaos check:** temporarily apply a policy denying Kafka egress from
  `meridian-core` → filing-api must **fail closed** and the `policy_denied`
  alert must fire (PagerDuty for enclave drops). Remove the chaos policy
  immediately after.
- `iperf3` east-west before/after — expect 10–30% lower p99 latency and no
  conntrack exhaustion (bpf lb, no iptables).
- mTLS still required: `curl https://filing-api:8443/v1/...` **without**
  client cert → 401. Proves the layered model (Cilium restricts
  reachability; mTLS authenticates identity).
- Hubble export: verify flows land in Kafka `audit.events.v1`
  (`rpk topic consume audit.events.v1 -n 10`) via the vector DaemonSet.
