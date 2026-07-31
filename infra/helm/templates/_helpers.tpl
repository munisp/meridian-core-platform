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
