{{- define "flightctl.getBaseDomain" }}
  {{- if .Values.global.baseDomain }}
    {{- printf .Values.global.baseDomain }}
  {{- else }}
    {{- /* For OpenShift deployments, try to lookup the base domain */}}
    {{- $dnsConfig := (lookup "config.openshift.io/v1" "DNS" "" "cluster") }}
    {{- if and $dnsConfig $dnsConfig.spec $dnsConfig.spec.baseDomain }}
      {{- $openShiftBaseDomain := $dnsConfig.spec.baseDomain }}
      {{- if .noNs }}
        {{- printf "apps.%s" $openShiftBaseDomain }}
      {{- else }}
        {{- printf "%s.apps.%s" .Release.Namespace $openShiftBaseDomain }}
      {{- end }}
    {{- else }}
      {{- fail "Unable to determine base domain. Please set global.baseDomain or deploy on OpenShift" }}
    {{- end }}
  {{- end }}
{{- end }}

{{- /*
Application name helper with optional override.
*/}}
{{- define "flightctl.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- /*
Standard Kubernetes/Helm recommended labels.
Usage: {{- include "flightctl.standardLabels" . | nindent X }}
*/}}
{{- define "flightctl.standardLabels" -}}
app.kubernetes.io/name: {{ include "flightctl.name" . }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion }}
{{- end }}
{{- end -}}

{{- define "flightctl.getOpenShiftAPIUrl" }}
  {{- if .Values.global.auth.k8s.externalOpenShiftApiUrl }}
    {{- printf .Values.global.auth.k8s.externalOpenShiftApiUrl }}
  {{- else if .Values.global.apiUrl }}
    {{- printf .Values.global.apiUrl }}
  {{- else if .Values.global.auth.k8s.apiUrl }}
    {{- printf .Values.global.auth.k8s.apiUrl }}
  {{- else }}
    {{- /* For OpenShift deployments, try to lookup the API URL */}}
    {{- $dnsConfig := (lookup "config.openshift.io/v1" "DNS" "" "cluster") }}
    {{- if and $dnsConfig $dnsConfig.spec $dnsConfig.spec.baseDomain }}
      {{- printf "https://api.%s:6443" $dnsConfig.spec.baseDomain }}
    {{- else }}
      {{- fail "Unable to determine API URL. Please set global.auth.k8s.externalOpenShiftApiUrl, global.apiUrl, global.auth.k8s.apiUrl, or deploy on OpenShift" }}
    {{- end }}
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
    {{- if and (eq $scheme "http") (not (eq (int .Values.global.gatewayPorts.http) 80))}}
      {{- printf "%s://ui.%s:%v" $scheme $baseDomain .Values.global.gatewayPorts.http }}
    {{- else if and (eq $scheme "https") (not (eq (int .Values.global.gatewayPorts.tls) 443))}}
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
  {{- else if and (eq (include "flightctl.getServiceExposeMethod" .) "gateway") (not (eq (int .Values.global.gatewayPorts.tls) 443)) }}
    {{- printf "https://api.%s:%v" $baseDomain .Values.global.gatewayPorts.tls }}
  {{- else }}
    {{- printf "https://api.%s" $baseDomain }}
  {{- end }}
{{- end }}

{{- define "flightctl.getOidcAuthorityUrl" }}
  {{- if .Values.global.auth.oidc.externalOidcAuthority }}
    {{- printf .Values.global.auth.oidc.externalOidcAuthority }}
  {{- else if .Values.global.auth.oidc.issuer }}
    {{- printf .Values.global.auth.oidc.issuer }}
  {{- else }}
    {{- include "flightctl.getApiUrl" . }}
  {{- end }}
{{- end }}

{{- /*
Get the effective auth type, translating 'builtin' to 'oidc' for backwards compatibility.
Usage: {{- $authType := include "flightctl.getEffectiveAuthType" . }}
*/}}
{{- define "flightctl.getEffectiveAuthType" }}
  {{- if eq .Values.global.auth.type "builtin" }}
    {{- print "oidc" }}
  {{- else }}
    {{- print .Values.global.auth.type }}
  {{- end }}
{{- end }}


{{- define "flightctl.getInternalCliArtifactsUrl" }}
  {{- print "http://flightctl-cli-artifacts:8090"}}
{{- end }}

{{- define "flightctl.getCliArtifactsUrl" }}
  {{- $baseDomain := (include "flightctl.getBaseDomain" . )}}
  {{- $scheme := (include "flightctl.getHttpScheme" . )}}
  {{- $exposeMethod := (include "flightctl.getServiceExposeMethod" . )}}
  {{- if eq $exposeMethod "nodePort" }}
    {{- printf "%s://%s:%v" $scheme $baseDomain .Values.global.nodePorts.cliArtifacts }}
  {{- else if eq $exposeMethod "gateway" }}
    {{- if and (eq $scheme "http") (not (eq (int .Values.global.gatewayPorts.http) 80))}}
      {{- printf "%s://cli-artifacts.%s:%v" $scheme $baseDomain .Values.global.gatewayPorts.http }}
    {{- else if and (eq $scheme "https") (not (eq (int .Values.global.gatewayPorts.tls) 443))}}
      {{- printf "%s://cli-artifacts.%s:%v" $scheme $baseDomain .Values.global.gatewayPorts.tls }}
    {{- else }}
      {{- printf "%s://cli-artifacts.%s" $scheme $baseDomain }}
    {{- end }}
  {{- else }}
    {{- printf "%s://cli-artifacts.%s" $scheme $baseDomain }}
  {{- end }}
{{- end }}

{{- define "flightctl.getAlertManagerProxyUrl" }}
  {{- $baseDomain := (include "flightctl.getBaseDomain" . )}}
  {{- $scheme := (include "flightctl.getHttpScheme" . )}}
  {{- $exposeMethod := (include "flightctl.getServiceExposeMethod" . )}}
  {{- if eq $exposeMethod "nodePort" }}
    {{- printf "%s://flightctl-alertmanager-proxy:8443" $scheme }}
  {{- else if eq $exposeMethod "gateway" }}
    {{- if and (eq $scheme "http") (not (eq (int .Values.global.gatewayPorts.http) 80))}}
      {{- printf "%s://alertmanager-proxy.%s:%v" $scheme $baseDomain .Values.global.gatewayPorts.http }}
    {{- else if and (eq $scheme "https") (not (eq (int .Values.global.gatewayPorts.tls) 443))}}
      {{- printf "%s://alertmanager-proxy.%s:%v" $scheme $baseDomain .Values.global.gatewayPorts.tls }}
    {{- else }}
      {{- printf "%s://alertmanager-proxy.%s" $scheme $baseDomain }}
    {{- end }}
  {{- else }}
    {{- printf "%s://alertmanager-proxy.%s" $scheme $baseDomain }}
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

{{/*
Database hostname helper.
Returns the database hostname, either from values or the default cluster service name.
*/}}
{{- define "flightctl.dbHostname" }}
{{- if eq .Values.db.external "enabled" -}}
{{ .Values.db.hostname }}
{{- else -}}
{{- default (printf "flightctl-db.%s.svc.cluster.local" (default .Release.Namespace .Values.global.internalNamespace)) .Values.db.hostname }}
{{- end }}
{{- end }}

{{/*
Database wait init container template.
Usage: {{- include "flightctl.databaseWaitInitContainer" (dict "context" .) | nindent 6 }}
Parameters:
- context: The root template context (.)
- userType: "app" (default), "migration", or "admin" (determines which secret to use)
- timeout: Optional timeout in seconds (default: 180)
- sleep: Optional sleep interval in seconds (default: 2)
- connectionTimeout: Optional connection timeout in seconds (default: 3)
*/}}
{{- define "flightctl.databaseWaitInitContainer" }}
{{- $context := .context }}
{{- $userType := .userType | default "app" }}
{{- $timeout := .timeout | default $context.Values.dbSetup.wait.timeout | default 60 | int }}
{{- $sleep := .sleep | default $context.Values.dbSetup.wait.sleep | default 2 | int }}
{{- $connectionTimeout := .connectionTimeout | default $context.Values.dbSetup.wait.connectionTimeout | default 3 | int }}
- name: wait-for-database-{{ $userType }}
  image: "{{ $context.Values.dbSetup.image.image }}:{{ default $context.Chart.AppVersion $context.Values.dbSetup.image.tag }}"
  imagePullPolicy: {{ default $context.Values.global.imagePullPolicy $context.Values.dbSetup.image.pullPolicy }}
  command:
  - /app/deploy/scripts/wait-for-database.sh
  {{- if ne $timeout 60 }}
  - "--timeout={{ $timeout }}"
  {{- end }}
  {{- if ne $sleep 2 }}
  - "--sleep={{ $sleep }}"
  {{- end }}
  {{- if ne $connectionTimeout 3 }}
  - "--connection-timeout={{ $connectionTimeout }}"
  {{- end }}
  env:
  - name: DB_USER_TYPE
    value: "{{ $userType }}"
  - name: DB_HOST
    value: "{{ include "flightctl.dbHostname" $context }}"
  - name: DB_PORT
    value: "{{ $context.Values.db.port }}"
  - name: DB_NAME
    value: "{{ $context.Values.db.name }}"
  {{- if eq $userType "app" }}
  - name: DB_USER
    valueFrom:
      secretKeyRef:
        name: flightctl-db-app-secret
        key: user
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: flightctl-db-app-secret
        key: userPassword
  {{- else if eq $userType "migration" }}
  - name: DB_USER
    valueFrom:
      secretKeyRef:
        name: flightctl-db-migration-secret
        key: migrationUser
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: flightctl-db-migration-secret
        key: migrationPassword
  {{- else if eq $userType "admin" }}
  - name: DB_USER
    valueFrom:
      secretKeyRef:
        name: flightctl-db-admin-secret
        key: masterUser
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: flightctl-db-admin-secret
        key: masterPassword
  {{- else }}
  {{- fail (printf "Invalid userType '%s'. Must be one of: app, migration, admin" $userType) }}
  {{- end }}
  {{- if $context.Values.db.sslmode }}
  - name: DB_SSL_MODE
    value: "{{ $context.Values.db.sslmode }}"
  {{- end }}
  {{- if $context.Values.db.sslcert }}
  - name: DB_SSL_CERT
    value: "{{ $context.Values.db.sslcert }}"
  {{- end }}
  {{- if $context.Values.db.sslkey }}
  - name: DB_SSL_KEY
    value: "{{ $context.Values.db.sslkey }}"
  {{- end }}
  {{- if $context.Values.db.sslrootcert }}
  - name: DB_SSL_ROOT_CERT
    value: "{{ $context.Values.db.sslrootcert }}"
  {{- end }}
  volumeMounts:
  {{- include "flightctl.dbSslVolumeMounts" $context | nindent 2 }}
{{- end }}

{{/*
Migration wait init container template.
Waits for database migration job to complete before starting the main container.
Usage: {{- include "flightctl.migrationWaitInitContainer" (dict "context" .) | nindent 6 }}
Parameters:
- context: The root template context (.)
- timeout: Optional timeout in seconds (default: 600)
*/}}
{{- define "flightctl.migrationWaitInitContainer" }}
{{- $ctx := .context }}
{{- $timeout := .timeout | default 600 | int }}
- name: wait-for-migration
  image: "{{ $ctx.Values.clusterCli.image.image }}:{{ $ctx.Values.clusterCli.image.tag }}"
  imagePullPolicy: {{ default $ctx.Values.global.imagePullPolicy $ctx.Values.clusterCli.image.pullPolicy }}
  command:
  - /bin/bash
  - -c
  - |
    set -euo pipefail

    LABEL_SELECTOR="app=flightctl-db-migration,flightctl.io/migration-revision={{ $ctx.Release.Revision }}"
    TIMEOUT={{ $timeout }}
    NS="{{ default $ctx.Release.Namespace $ctx.Values.global.internalNamespace }}"

    echo "Waiting for migration job with labels: $LABEL_SELECTOR (timeout ${TIMEOUT}s)"
    start=$(date +%s)

    while true; do
      elapsed=$(( $(date +%s) - start ))

      if [ $elapsed -ge $TIMEOUT ]; then
        echo "Timeout waiting for migration job after ${TIMEOUT}s"
        exit 1
      fi

      # Find job by label selector
      JOB=$(kubectl get jobs -n "$NS" -l "$LABEL_SELECTOR" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
      
      # Check if job exists
      if [ -n "$JOB" ]; then
        # Check for parallel execution (dangerous even with idempotent migrations)
        parallelism=$(kubectl get job "$JOB" -n "$NS" -o jsonpath='{.spec.parallelism}' 2>/dev/null || echo 1)
        if (( parallelism > 1 )); then
            echo "ERROR: Migration job has spec.parallelism=$parallelism (must be 1)"
            echo "Database migrations must not run in parallel due to race condition risks"
            exit 1
        fi

        succeeded=$(kubectl get job "$JOB" -n "$NS" -o jsonpath='{.status.succeeded}' 2>/dev/null || echo 0)
        failed=$(kubectl get job "$JOB" -n "$NS" -o jsonpath='{.status.failed}' 2>/dev/null || echo 0)

        if (( succeeded > 0 )); then
            # The 'greater than 1' scenario would only occur if the job spec.completions is set to more than 1
            if (( succeeded > 1 )); then
                echo "Warning: Migration job completed $succeeded times (expected 1). Ensure spec.completions is set to 1."
            fi
            echo "Migration job $JOB completed successfully"
            exit 0
        elif (( failed > 0 )); then
            echo "Migration job $JOB failed"
            exit 1
        else
            echo "Migration job $JOB is still running..."
        fi
      else
        # Job not found - could be not created yet, RBAC issue, or other problem
        echo "Migration job not found yet, waiting..."
      fi

      sleep 5
    done
{{- end }}

{{- /*
SSL certificate volume mounts for database connections.
Usage: {{- include "flightctl.dbSslVolumeMounts" . | nindent X }}
*/}}
{{- define "flightctl.dbSslVolumeMounts" -}}
{{- if or .Values.db.sslConfigMap .Values.db.sslSecret }}
- name: postgres-ssl-certs
  mountPath: /etc/ssl/postgres
  readOnly: true
{{- end }}
{{- end }}

{{- /*
SSL certificate volumes for database connections.
Usage: {{- include "flightctl.dbSslVolumes" . | nindent X }}
*/}}
{{- define "flightctl.dbSslVolumes" -}}
{{- if or .Values.db.sslConfigMap .Values.db.sslSecret }}
- name: postgres-ssl-certs
  projected:
    sources:
    {{- if .Values.db.sslConfigMap }}
    - configMap:
        name: {{ .Values.db.sslConfigMap }}
        items:
        - key: ca-cert.pem
          path: ca-cert.pem
          mode: 0444
    {{- end }}
    {{- if .Values.db.sslSecret }}
    - secret:
        name: {{ .Values.db.sslSecret }}
        items:
        - key: client-cert.pem
          path: client-cert.pem
          mode: 0444
        - key: client-key.pem
          path: client-key.pem
          mode: 0400
    {{- end }}
{{- end }}
{{- end }}
