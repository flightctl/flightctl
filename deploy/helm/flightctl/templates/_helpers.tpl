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
  {{- if .Values.global.auth.k8s.externalOpenShiftApiUrl }}
    {{- printf .Values.global.auth.k8s.externalOpenShiftApiUrl }}
  {{- else if .Values.global.apiUrl }}
    {{- printf .Values.global.apiUrl }}
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
    {{- else if and (eq $scheme "https") (not (eq int (.Values.global.gatewayPorts.tls) 443))}}
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
  {{- else if and (eq (include "flightctl.getServiceExposeMethod" .) "gateway") (not (eq int .Values.global.gatewayPorts.tls 443)) }}
    {{- printf "https://api.%s:%v" $baseDomain .Values.global.gatewayPorts.tls }}
  {{- else }}
    {{- printf "https://api.%s" $baseDomain }}
  {{- end }}
{{- end }}

{{- define "flightctl.getOidcAuthorityUrl" }}
  {{- if .Values.global.auth.oidc.externalOidcAuthority }}
    {{- printf .Values.global.auth.oidc.externalOidcAuthority }}
  {{- else }}
    {{- $baseDomain := (include "flightctl.getBaseDomain" . )}}
    {{- $scheme := (include "flightctl.getHttpScheme" . )}}
    {{- $exposeMethod := (include "flightctl.getServiceExposeMethod" .)}}
    {{- if eq $exposeMethod "nodePort" }}
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
{{- end }}

{{/*
Generates a random alphanumeric password in the format xxxxx-xxxxx-xxxxx-xxxxx.
*/}}
{{- define "flightctl.generatePassword" }}
{{- $password := (randAlphaNum 20) }}
{{- $pass := printf "%s-%s-%s-%s" (substr 0 5 $password) (substr 5 10 $password) (substr 10 15 $password) (substr 15 20 $password) }}
{{- print ($pass | b64enc) }}
{{- end }}
