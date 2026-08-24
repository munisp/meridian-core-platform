{{- /*
Effective per-service config: deep-merge .Values.defaults with the service
override block. Usage: $cfg := include "meridian.serviceConfig" (dict "root" $ "svc" $svc) | fromYaml
*/ -}}
{{- define "meridian.serviceConfig" -}}
{{- $cfg := mergeOverwrite (deepCopy .root.Values.defaults) .svc -}}
{{- toYaml $cfg -}}
{{- end -}}
{{- define "meridian.fullname" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- /*
Baseline pod + container security context (A8 hardening). Applied by every
workload template; overridable per-service via .Values.<svc>.securityContext.
*/ -}}
{{- define "meridian.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: 10001
runAsGroup: 10001
fsGroup: 10001
seccompProfile:
  type: RuntimeDefault
{{- end -}}
{{- define "meridian.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
{{- end -}}
{{- /*
In-repo service ports (single source of truth: the service binds in
meridian-core-platform source; see docs/service-map / api/*.yaml). Services
not listed here render no Deployment/Service and must carry enabled: false
in values.
*/ -}}
{{- define "meridian.servicePort" -}}
{{- $ports := dict
  "admin-api" 8095
  "audit-evidence" 8004
  "consent" 8007
  "edge-policy" 8009
  "feature-store" 8012
  "geo" 8005
  "ledger" 8010
  "migration" 8011
  "notification" 8006
  "reg-watch" 8014
  "rp-registry" 8002
  "rules-engine" 8001
  "search-indexer" 8008
  "settlement" 8013
  "tin-graph" 8003
-}}
{{- get $ports . -}}
{{- end -}}
