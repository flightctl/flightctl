{{- define "test-app.containerSecurityContext" -}}
securityContext:
  allowPrivilegeEscalation: false
  capabilities:
    drop:
      - ALL
  runAsNonRoot: true
  # Leave runAsUser unset so OpenShift's restricted SCC can assign a UID from
  # the namespace's permitted range.
  seccompProfile:
    type: RuntimeDefault
{{- end -}}
