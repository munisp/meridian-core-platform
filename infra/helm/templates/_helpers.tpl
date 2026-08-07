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
