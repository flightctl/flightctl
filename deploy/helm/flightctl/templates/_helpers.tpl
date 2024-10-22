{{- define "flightctl.getBaseDomain" }}
  {{- if .Values.global.baseDomain }}
    {{- printf .Values.global.baseDomain }}
  {{- else }}
    {{- $openShiftBaseDomain := (lookup "config.openshift.io/v1" "DNS" "" "cluster").spec.baseDomain }}
    {{- if .noNs }}
      {{- printf "apps.%s" $openShiftBaseDomain }}
    {{- else }}
      {{- printf "%s.apps.%s" .Release.Namespace $openShiftBaseDomain }}
    {{- end }}
  {{- end }}
{{- end }}

{{- define "flightctl.getOpenShiftAPIUrl" }}
  {{- if .Values.global.auth.openShiftApiUrl }}
    {{- printf .Values.global.auth.openShiftApiUrl }}
  {{- else }}
    {{- $openShiftBaseDomain := (lookup "config.openshift.io/v1" "DNS" "" "cluster").spec.baseDomain }}
    {{- printf "https://api.%s:6443" $openShiftBaseDomain }}
  {{- end }}
{{- end }}

{{- define "flightctl.getUIHttpScheme" }}
  {{- if or (or (eq .Values.global.target "acm") (eq .Values.global.exposeServicesMethod "route")) (.Values.global.baseDomainTls).cert }}
    {{- printf "https" }}
  {{- else }}
    {{- printf "http" }}
  {{- end }}
{{- end }}

{{- define "flightctl.getUIUrl" }}
  {{- $scheme := (include "flightctl.getUIHttpScheme" .) }}
  {{- if eq .Values.global.target "acm" }}
    {{- $baseDomain := (include "flightctl.getBaseDomain" (deepCopy . | merge (dict "noNs" "true"))) }}
    {{- printf "%s://console-openshift-console.%s/edge" $scheme $baseDomain }}
  {{- else if eq (include "flightctl.getServiceExposeMethod" .) "nodePort" }}
    {{- $baseDomain := (include "flightctl.getBaseDomain" . )}}
    {{- printf "%s://%s:%v" $scheme $baseDomain .Values.global.nodePorts.ui }} 
  {{- else }}
    {{- $baseDomain := (include "flightctl.getBaseDomain" . )}}
    {{- printf "%s://ui.%s" $scheme $baseDomain }}
  {{- end }}
{{- end }}

{{- define "flightctl.getServiceExposeMethod" }}
  {{- if or (eq .Values.global.target "acm") (eq .Values.global.exposeServicesMethod "route")}}
    {{- printf "route" }}
  {{- else }}
    {{- printf "nodePort" }}
  {{- end}}
{{- end }}

{{- define "flightctl.getApiUrl" }}
  {{- $baseDomain := (include "flightctl.getBaseDomain" . )}}
  {{- if eq (include "flightctl.getServiceExposeMethod" .) "route" }}
    {{- printf "https://api.%s" $baseDomain }}
  {{- else }}
    {{- printf "https://%s:%v" $baseDomain .Values.global.nodePorts.api }} 
  {{- end }}
{{- end }}
