{{- define "flightctl.enableOpenShiftExtensions" }}
  {{- $isOpenShift := "false" }}
  {{- if eq .Values.global.enableOpenShiftExtensions "true" }}
    {{- $isOpenShift = "true" }}
  {{- else if eq .Values.global.enableOpenShiftExtensions "auto" }}
    {{- if .Capabilities.APIVersions.Has "config.openshift.io/v1" -}}
      {{- $isOpenShift = "true" }}
    {{- end }}
  {{- end }}
  {{- $isOpenShift }}
{{- end }}

{{- define "flightctl.enableMulticlusterExtensions" }}
  {{- $enabled := "false" }}
  {{- if eq .Values.global.enableMulticlusterExtensions "true" }}
    {{- $enabled = "true" }}
  {{- else if eq .Values.global.enableMulticlusterExtensions "auto" }}
    {{- /* Check if the MultiClusterEngine CRD exists */}}
    {{- if .Capabilities.APIVersions.Has "multicluster.openshift.io/v1" -}}
      {{- $enabled = "true" }}
    {{- end }}
  {{- end }}
  {{- $enabled }}
{{- end }}

{{- define "flightctl.getBaseDomain" }}
  {{- $baseDomain := "" }}
  {{- if .Values.global.baseDomain }}
    {{- $baseDomain = .Values.global.baseDomain }}
  {{- else }}
    {{- $isOpenShift := (include "flightctl.enableOpenShiftExtensions" . )}}
    {{- if eq $isOpenShift "true"}}
      {{- /* For OpenShift deployments, try to lookup the base domain */}}
      {{- $dnsConfig := (lookup "config.openshift.io/v1" "DNS" "" "cluster") }}
      {{- if and $dnsConfig $dnsConfig.spec $dnsConfig.spec.baseDomain }}
        {{- $openShiftBaseDomain := $dnsConfig.spec.baseDomain }}
        {{- if .noNs }}
          {{- $baseDomain = printf "apps.%s" $openShiftBaseDomain }}
        {{- else }}
          {{- $baseDomain = printf "%s.apps.%s" .Release.Namespace $openShiftBaseDomain }}
        {{- end }}
      {{- end }}
    {{- end }}
  {{- end }}
  {{- if empty $baseDomain }}
    {{- fail "Unable to determine base domain. Please set global.baseDomain or deploy on OpenShift" }}
  {{- end }}
  {{- $baseDomain }}
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

{{- define "flightctl.getOpenShiftOAuthServerUrl" }}
  {{- /* Returns the OpenShift OAuth server URL by looking up the oauth-openshift route */}}
  {{- $oauthRoute := (lookup "route.openshift.io/v1" "Route" "openshift-authentication" "oauth-openshift") }}
  {{- if and $oauthRoute $oauthRoute.spec $oauthRoute.spec.host }}
    {{- printf "https://%s" $oauthRoute.spec.host }}
  {{- else }}
    {{- fail "Unable to find oauth-openshift route in openshift-authentication namespace. For HyperShift or non-standard OpenShift clusters, you must manually set authorizationUrl, tokenUrl, and issuer in global.auth.openshift" }}
  {{- end }}
{{- end }}

{{- define "flightctl.getOpenShiftOAuthAuthorizationUrl" }}
  {{- if .Values.global.auth.openshift.authorizationUrl }}
    {{- printf .Values.global.auth.openshift.authorizationUrl }}
  {{- else }}
    {{- $oauthUrl := (include "flightctl.getOpenShiftOAuthServerUrl" .) }}
    {{- printf "%s/oauth/authorize" $oauthUrl }}
  {{- end }}
{{- end }}

{{- define "flightctl.getOpenShiftOAuthTokenUrl" }}
  {{- if .Values.global.auth.openshift.tokenUrl }}
    {{- printf .Values.global.auth.openshift.tokenUrl }}
  {{- else }}
    {{- $oauthUrl := (include "flightctl.getOpenShiftOAuthServerUrl" .) }}
    {{- printf "%s/oauth/token" $oauthUrl }}
  {{- end }}
{{- end }}

{{- define "flightctl.getOpenShiftOAuthIssuer" }}
  {{- if .Values.global.auth.openshift.issuer }}
    {{- printf .Values.global.auth.openshift.issuer }}
  {{- else if .Values.global.auth.openshift.authorizationUrl }}
    {{- printf .Values.global.auth.openshift.authorizationUrl }}
  {{- else }}
    {{- $oauthUrl := (include "flightctl.getOpenShiftOAuthServerUrl" .) }}
    {{- printf "%s/oauth/authorize" $oauthUrl }}
  {{- end }}
{{- end }}

{{- define "flightctl.getOpenShiftOAuthClientId" }}
  {{- if .Values.global.auth.openshift.clientId }}
    {{- printf .Values.global.auth.openshift.clientId }}
  {{- else }}
    {{- printf "flightctl-%s" .Release.Name }}
  {{- end }}
{{- end }}

{{- define "flightctl.getOpenShiftProjectLabelFilter" }}
  {{- if .Values.global.auth.openshift.projectLabelFilter }}
    {{- printf .Values.global.auth.openshift.projectLabelFilter }}
  {{- else }}
    {{- printf "io.flightctl/instance=%s" .Release.Name }}
  {{- end }}
{{- end }}

{{/*
Get the OAuth client secret from values or lookup existing secret.
Uses a cached value in .Values to ensure consistency across all template evaluations.
*/}}
{{- define "flightctl.getOpenShiftOAuthClientSecret" }}
  {{- if .Values.global.auth.openshift.clientSecret }}
    {{- .Values.global.auth.openshift.clientSecret }}
  {{- else }}
    {{- $existingOAuthClient := (lookup "oauth.openshift.io/v1" "OAuthClient" "" (include "flightctl.getOpenShiftOAuthClientId" .)) }}
    {{- if $existingOAuthClient }}
      {{- if $existingOAuthClient.secret }}
        {{- $existingOAuthClient.secret }}
      {{- else }}
        {{- fail (printf "OAuthClient %s is missing secret – delete it or add the key." (include "flightctl.getOpenShiftOAuthClientId" .)) }}
      {{- end }}
    {{- else }}
      {{- if not (hasKey .Values "__generatedOAuthSecret") }}
        {{- $_ := set .Values "__generatedOAuthSecret" (randAlphaNum 32) }}
      {{- end }}
      {{- .Values.__generatedOAuthSecret -}}
    {{- end }}
  {{- end }}
{{- end }}

{{- define "flightctl.getHttpScheme" }}
  {{- if or (eq (include "flightctl.getServiceExposeMethod" . ) "route") .Values.global.baseDomainTlsSecretName }}
    {{- printf "https" }}
  {{- else }}
    {{- printf "http" }}
  {{- end }}
{{- end }}

{{- define "flightctl.getUIUrl" }}
  {{- $scheme := (include "flightctl.getHttpScheme" .) }}
  {{- $baseDomain := (include "flightctl.getBaseDomain" . )}}
  {{- $enableMulticlusterExtensions := (include "flightctl.enableMulticlusterExtensions" . )}}
  {{- if eq $enableMulticlusterExtensions "true" }}
    {{- $baseDomain := (include "flightctl.getBaseDomain" (deepCopy . | merge (dict "noNs" "true"))) }}
    {{- printf "%s://console-openshift-console.%s/edge" $scheme $baseDomain }}
  {{- else if eq (include "flightctl.getServiceExposeMethod" .) "nodePort" }}
    {{- printf "%s://%s:%v" $scheme $baseDomain .Values.dev.nodePorts.ui }}
  {{- else if eq (include "flightctl.getServiceExposeMethod" .) "gateway" }}
    {{- if and (eq $scheme "http") (not (eq (int .Values.global.gateway.ports.http) 80))}}
      {{- printf "%s://ui.%s:%v" $scheme $baseDomain .Values.global.gateway.ports.http }}
    {{- else if and (eq $scheme "https") (not (eq (int .Values.global.gateway.ports.tls) 443))}}
      {{- printf "%s://ui.%s:%v" $scheme $baseDomain .Values.global.gateway.ports.tls }}
    {{- else }}
      {{- printf "%s://ui.%s" $scheme $baseDomain }}
    {{- end }}
  {{- else }}
    {{- printf "%s://ui.%s" $scheme $baseDomain }}
  {{- end }}
{{- end }}

{{- define "flightctl.getServiceExposeMethod" }}
  {{- $exposeMethod := .Values.global.exposeServicesMethod }}
  {{- if eq (default dict .Values.dev).exposeServicesMethod "nodePort" }}
    {{- $exposeMethod = "nodePort" }}
  {{- else }}
    {{- if eq $exposeMethod "auto" }}
      {{- $isOpenShift := (include "flightctl.enableOpenShiftExtensions" . )}}
      {{- if eq $isOpenShift "true" }}
        {{- $exposeMethod = "route" }}
      {{- else if .Capabilities.APIVersions.Has "gateway.networking.k8s.io/v1" -}}
        {{- $exposeMethod = "gateway" }}
      {{- else }}
        {{- fail "Could not detect OpenShift, nor Gateway resources. Please set global.exposeServicesMethod" }}
      {{- end }}
    {{- end }}
  {{- end }}
  {{- $exposeMethod }}
{{- end }}

{{- define "flightctl.getApiUrl" }}
  {{- $baseDomain := (include "flightctl.getBaseDomain" . )}}
  {{- if eq (include "flightctl.getServiceExposeMethod" .) "nodePort" }}
    {{- printf "https://%s:%v" $baseDomain .Values.dev.nodePorts.api }}
  {{- else if and (eq (include "flightctl.getServiceExposeMethod" .) "gateway") (not (eq (int .Values.global.gateway.ports.tls) 443)) }}
    {{- printf "https://api.%s:%v" $baseDomain .Values.global.gateway.ports.tls }}
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
Get the effective auth type with auto-detection:
- Auto-detects OpenShift and sets type to "openshift" if not explicitly set
- Falls back to k8s if no auth type specified and not on OpenShift
Usage: {{- $authType := include "flightctl.getEffectiveAuthType" . }}
*/}}
{{- define "flightctl.getEffectiveAuthType" }}
  {{- if .Values.global.auth.type }}
    {{- print .Values.global.auth.type }}
  {{- else }}
    {{- /* Auto-detect: if on OpenShift, use openshift auth, otherwise k8s */}}
    {{- if eq (include "flightctl.enableOpenShiftExtensions" .) "true" }}
      {{- print "openshift" }}
    {{- else }}
      {{- print "k8s" }}
    {{- end }}
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
    {{- printf "%s://%s:%v" $scheme $baseDomain .Values.dev.nodePorts.cliArtifacts }}
  {{- else if eq $exposeMethod "gateway" }}
    {{- if and (eq $scheme "http") (not (eq (int .Values.global.gateway.ports.http) 80))}}
      {{- printf "%s://cli-artifacts.%s:%v" $scheme $baseDomain .Values.global.gateway.ports.http }}
    {{- else if and (eq $scheme "https") (not (eq (int .Values.global.gateway.ports.tls) 443))}}
      {{- printf "%s://cli-artifacts.%s:%v" $scheme $baseDomain .Values.global.gateway.ports.tls }}
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
    {{- printf "https://flightctl-alertmanager-proxy:8443" }}
  {{- else if eq $exposeMethod "gateway" }}
    {{- if and (eq $scheme "http") (not (eq (int .Values.global.gateway.ports.http) 80))}}
      {{- printf "%s://alertmanager-proxy.%s:%v" $scheme $baseDomain .Values.global.gateway.ports.http }}
    {{- else if and (eq $scheme "https") (not (eq (int .Values.global.gateway.ports.tls) 443))}}
      {{- printf "%s://alertmanager-proxy.%s:%v" $scheme $baseDomain .Values.global.gateway.ports.tls }}
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
{{- if eq .Values.db.type "external" }}
  {{- .Values.db.external.hostname }}
{{- else }}
  {{- printf "flightctl-db.%s.svc.cluster.local" (default .Release.Namespace .Values.global.internalNamespace) }}
{{- end }}
{{- end }}

{{/*
Database port helper.
Returns the database port, either from values or the default cluster service port.
*/}}
{{- define "flightctl.dbPort" }}
{{- if eq .Values.db.type "external" }}
  {{- .Values.db.external.port }}
{{- else }}
  {{- 5432 }}
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
    value: "{{ include "flightctl.dbPort" $context }}"
  - name: DB_NAME
    value: "{{ $context.Values.db.name }}"
  {{- if eq $userType "app" }}
  - name: DB_USER
    valueFrom:
      secretKeyRef:
        name: {{ include "flightctl.dbAppUserSecret" $context }}
        key: user
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: {{ include "flightctl.dbAppUserSecret" $context }}
        key: userPassword
  {{- else if eq $userType "migration" }}
  - name: DB_USER
    valueFrom:
      secretKeyRef:
        name: {{ include "flightctl.dbMigrationUserSecret" $context }}
        key: migrationUser
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: {{ include "flightctl.dbMigrationUserSecret" $context }}
        key: migrationPassword
  {{- else if eq $userType "admin" }}
  - name: DB_USER
    valueFrom:
      secretKeyRef:
        name: {{ default "flightctl-db-admin-secret" $context.Values.db.builtin.masterUserSecretName }}
        key: masterUser
  - name: DB_PASSWORD
    valueFrom:
      secretKeyRef:
        name: {{ default "flightctl-db-admin-secret" $context.Values.db.builtin.masterUserSecretName }}
        key: masterPassword
  {{- else }}
  {{- fail (printf "Invalid userType '%s'. Must be one of: app, migration, admin" $userType) }}
  {{- end }}
  {{- if eq $context.Values.db.type "external" }}
  {{- if $context.Values.db.external.sslmode }}
  - name: DB_SSL_MODE
    value: "{{ $context.Values.db.external.sslmode }}"
  {{- end }}
  {{- if $context.Values.db.external.tlsSecretName }}
  - name: DB_SSL_CERT
    value: /etc/ssl/postgres/client-cert.pem
  - name: DB_SSL_KEY
    value: /etc/ssl/postgres/client-key.pem
  {{- end }}
  {{- if $context.Values.db.external.tlsConfigMapName }}
  - name: DB_SSL_ROOT_CERT
    value: /etc/ssl/postgres/ca-cert.pem
  {{- end }}
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

    LABEL_SELECTOR="flightctl.service=flightctl-db-migration,flightctl.io/migration-revision={{ $ctx.Release.Revision }}"
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
{{- if and (eq .Values.db.type "external") (or .Values.db.external.tlsConfigMapName .Values.db.external.tlsSecretName) }}
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
{{- if and (eq .Values.db.type "external") (or .Values.db.external.tlsConfigMapName .Values.db.external.tlsSecretName) }}
- name: postgres-ssl-certs
  projected:
    sources:
    {{- if .Values.db.external.tlsConfigMapName }}
    - configMap:
        name: {{ .Values.db.external.tlsConfigMapName }}
        items:
        - key: ca-cert.pem
          path: ca-cert.pem
          mode: 0444
    {{- end }}
    {{- if .Values.db.external.tlsSecretName }}
    - secret:
        name: {{ .Values.db.external.tlsSecretName }}
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

{{- define "flightctl.dbAppUserSecret" -}}
{{- if eq .Values.db.type "external" }}
  {{- .Values.db.external.applicationUserSecretName }}
{{- else }}
  {{- default "flightctl-db-app-secret" .Values.db.builtin.applicationUserSecretName }}
{{- end }}
{{- end }}

{{- define "flightctl.dbMigrationUserSecret" -}}
{{- if eq .Values.db.type "external" }}
  {{- .Values.db.external.migrationUserSecretName }}
{{- else }}
  {{- default "flightctl-db-migration-secret" .Values.db.builtin.migrationUserSecretName }}
{{- end }}
{{- end }}

{{- /*
Determine the effective certificate generation method.
Returns: "false", "cert-manager", or "builtin"
Usage: {{- $certMethod := include "flightctl.getCertificateGenerationMethod" . }}
*/}}
{{- define "flightctl.getCertificateGenerationMethod" }}
  {{- $method := .Values.global.generateCertificates | toString }}
  {{- if eq $method "auto" }}
    {{- if .Capabilities.APIVersions.Has "cert-manager.io/v1/Certificate" }}
      {{- print "cert-manager" }}
    {{- else }}
      {{- print "builtin" }}
    {{- end }}
  {{- else }}
    {{- print $method }}
  {{- end }}
{{- end }}

{{- /*
Get DNS SANs for flightctl-api server certificate
Usage: {{- $result := include "flightctl.getApiServerDNSSans" . | fromJson }}{{ $apiServerDNSSans := $result.sans }}
*/}}
{{- define "flightctl.getApiServerDNSSans" }}
  {{- $baseDomain := include "flightctl.getBaseDomain" . }}
  {{- $sans := list }}
  {{- $sans = append $sans (printf "api.%s" $baseDomain) }}
  {{- $sans = append $sans (printf "agent-api.%s" $baseDomain) }}
  {{- $sans = append $sans "flightctl-api" }}
  {{- $sans = append $sans (printf "flightctl-api.%s" .Release.Namespace) }}
  {{- $sans = append $sans (printf "flightctl-api.%s.svc.cluster.local" .Release.Namespace) }}
  {{- dict "sans" $sans | toJson -}}
{{- end }}

{{- /*
Get DNS SANs for telemetry-gateway server certificate
Usage: {{- $result := include "flightctl.getTelemetryGatewayDNSSans" . | fromJson }}{{ $telemetryGatewayDNSSans := $result.sans }}
*/}}
{{- define "flightctl.getTelemetryGatewayDNSSans" }}
  {{- $sans := list }}
  {{- $baseDomain := include "flightctl.getBaseDomain" . }}
  {{- $sans = append $sans (printf "telemetry.%s" $baseDomain) }}
  {{- $sans = append $sans "flightctl-telemetry-gateway" }}
  {{- $sans = append $sans (printf "flightctl-telemetry-gateway.%s" .Release.Namespace) }}
  {{- $sans = append $sans (printf "flightctl-telemetry-gateway.%s.svc.cluster.local" .Release.Namespace) }}
  {{- dict "sans" $sans | toJson -}}
{{- end }}

{{- /*
Get DNS SANs for alertmanager-proxy server certificate
Usage: {{- $result := include "flightctl.getAlertmanagerProxyDNSSans" . | fromJson }}{{ $alertmanagerProxyDNSSans := $result.sans }}
*/}}
{{- define "flightctl.getAlertmanagerProxyDNSSans" }}
  {{- $sans := list }}
  {{- $baseDomain := include "flightctl.getBaseDomain" . }}
  {{- $sans = append $sans (printf "alertmanager-proxy.%s" $baseDomain) }}
  {{- $sans = append $sans "flightctl-alertmanager-proxy" }}
  {{- $sans = append $sans (printf "flightctl-alertmanager-proxy.%s" .Release.Namespace) }}
  {{- $sans = append $sans (printf "flightctl-alertmanager-proxy.%s.svc.cluster.local" .Release.Namespace) }}
  {{- dict "sans" $sans | toJson -}}
{{- end }}
