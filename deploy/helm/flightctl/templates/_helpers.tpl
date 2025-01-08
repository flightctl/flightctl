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

{{- define "flightctl.getHttpScheme" }}
  {{- if or (or (eq .Values.global.target "acm") (eq .Values.global.exposeServicesMethod "route")) (.Values.global.baseDomainTls).cert }}
    {{- printf "https" }}
  {{- else }}
    {{- printf "http" }}
  {{- end }}
{{- end }}

{{- define "flightctl.getUIUrl" }}
  {{- $scheme := (include "flightctl.getHttpScheme" .) }}
  {{- $baseDomain := (include "flightctl.getBaseDomain" . )}}
  {{- if eq .Values.global.target "acm" }}
    {{- $baseDomain := (include "flightctl.getBaseDomain" (deepCopy . | merge (dict "noNs" "true"))) }}
    {{- printf "%s://console-openshift-console.%s/edge" $scheme $baseDomain }}
  {{- else if eq (include "flightctl.getServiceExposeMethod" .) "nodePort" }}
    {{- printf "%s://%s:%v" $scheme $baseDomain .Values.global.nodePorts.ui }} 
  {{- else if eq (include "flightctl.getServiceExposeMethod" .) "gateway" }}
    {{- if and (eq $scheme "http") (not (eq .Values.global.gatewayPorts.http 80))}}
      {{- printf "%s://ui.%s:%v" $scheme $baseDomain .Values.global.gatewayPorts.http }} 
    {{- else if and (eq $scheme "https") (not (eq .Values.global.gatewayPorts.tls 443))}}
      {{- printf "%s://ui.%s:%v" $scheme $baseDomain .Values.global.gatewayPorts.tls }} 
    {{- else }}
      {{- printf "%s://ui.%s" $scheme $baseDomain }}
    {{- end }}
  {{- else }}
    {{- printf "%s://ui.%s" $scheme $baseDomain }}
  {{- end }}
{{- end }}

{{- define "flightctl.getServiceExposeMethod" }}
  {{- if eq .Values.global.target "acm" }}
    {{- printf "route" }}
  {{- else }}
    {{- printf .Values.global.exposeServicesMethod }}
  {{- end}}
{{- end }}

{{- define "flightctl.getApiUrl" }}
  {{- $baseDomain := (include "flightctl.getBaseDomain" . )}}
  {{- if eq (include "flightctl.getServiceExposeMethod" .) "nodePort" }}
    {{- printf "https://%s:%v" $baseDomain .Values.global.nodePorts.api }} 
  {{- else if and (eq (include "flightctl.getServiceExposeMethod" .) "gateway") (not (eq .Values.global.gatewayPorts.tls 443)) }}
    {{- printf "https://api.%s:%v" $baseDomain .Values.global.gatewayPorts.tls }}
  {{- else }}
    {{- printf "https://api.%s" $baseDomain }}
  {{- end }}
{{- end }}

{{- define "flightctl.getOidcAuthorityUrl" }}
  {{- $baseDomain := (include "flightctl.getBaseDomain" . )}}
  {{- $scheme := (include "flightctl.getHttpScheme" . )}}
  {{- $exposeMethod := (include "flightctl.getServiceExposeMethod" .)}}
  {{- if .Values.global.auth.oidcAuthority }}
    {{- printf "%s" .Values.global.auth.oidcAuthority }}
  {{- else if eq $exposeMethod "nodePort" }}
    {{- printf "%s://auth.%s:%v/realms/flightctl" $scheme $baseDomain .Values.global.nodePorts.keycloak }}
  {{- else if eq $exposeMethod "gateway" }}
    {{- if and (eq $scheme "http") (not (eq .Values.global.gatewayPorts.http 80)) }}
      {{- printf "%s://auth.%s:%v/realms/flightctl" $scheme $baseDomain .Values.global.gatewayPorts.http }}
    {{- else if and (eq $scheme "https") (not (eq .Values.global.gatewayPorts.tls 443))}}
      {{- printf "%s://auth.%s:%v/realms/flightctl" $scheme $baseDomain .Values.global.gatewayPorts.tls }}
    {{- else }}
      {{- printf "%s://auth.%s/realms/flightctl" $scheme $baseDomain }}
    {{- end }}
  {{- else }}
    {{- printf "%s://auth.%s/realms/flightctl" $scheme $baseDomain }}
  {{- end }}
{{- end }}
