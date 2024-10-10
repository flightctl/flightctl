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


