{{/*
Renders the DB migration Job. Supports dry-run variant and hook customization.
Parameters:
- context: Root context (.)
- name: Job name
- hooks: List of hook phases (e.g., (list "pre-upgrade"))
- hookWeight: String weight for execution order
- hookDeletePolicy: Hook delete policy
- isDryRun: If true, runs migration with --dry-run and skips side-effect steps
*/}}
{{- define "flightctl.dbMigrationJob" }}
{{- $ctx := .context }}
{{- $name := .name }}
{{- $hooks := .hooks | default (list) }}
{{- $hookWeight := .hookWeight | default "10" }}
{{- $deletePolicy := .hookDeletePolicy | default "before-hook-creation" }}
{{- $isDryRun := .isDryRun | default false }}
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ $name }}
  namespace: {{ default $ctx.Release.Namespace $ctx.Values.global.internalNamespace }}
  labels:
    flightctl.service: flightctl-db-migration
    release: {{ $ctx.Release.Name }}
    flightctl.io/migration-revision: "{{ $ctx.Release.Revision }}"
    {{- include "flightctl.standardLabels" $ctx | nindent 4 }}
  annotations:
    {{- if gt (len $hooks) 0 }}
    helm.sh/hook: {{ join "," $hooks }}
    helm.sh/hook-weight: "{{ $hookWeight }}"
    helm.sh/hook-delete-policy: "{{ $deletePolicy }}"
    {{- end }}
spec:
  backoffLimit: {{ $ctx.Values.dbSetup.migration.backoffLimit | int }}
  {{- if gt ($ctx.Values.dbSetup.migration.activeDeadlineSeconds | int) 0 }}
  activeDeadlineSeconds: {{ $ctx.Values.dbSetup.migration.activeDeadlineSeconds | int }}
  {{- end }}
  completions: 1
  parallelism: 1
  template:
    metadata:
      labels:
        flightctl.service: flightctl-db-migration
        release: {{ $ctx.Release.Name }}
        flightctl.io/migration-revision: "{{ $ctx.Release.Revision }}"
        {{- include "flightctl.standardLabels" $ctx | nindent 8 }}
    spec:
      restartPolicy: OnFailure
      serviceAccountName: flightctl-db-migration
      initContainers:
      {{- $userType := ternary "admin" "migration" (eq $ctx.Values.db.type "builtin") }}
      {{- include "flightctl.databaseWaitInitContainer" (dict "context" $ctx "userType" $userType "timeout" 120 "sleep" 2) | nindent 6 }}
      {{- if eq $ctx.Values.db.type "builtin" }}
      {{- if not $isDryRun }}
      - name: setup-database-users
        image: "{{ include "flightctl.ensureOsQualifiedImage" $ctx.Values.dbSetup.image.image }}:{{ default $ctx.Chart.AppVersion $ctx.Values.dbSetup.image.tag }}"
        imagePullPolicy: {{ default $ctx.Values.global.imagePullPolicy $ctx.Values.dbSetup.image.pullPolicy }}
        env:
        - name: DB_HOST
          value: "{{ include "flightctl.dbHostname" $ctx }}"
        - name: DB_PORT
          value: "{{ include "flightctl.dbPort" $ctx }}"
        - name: DB_NAME
          value: "{{ $ctx.Values.db.name }}"
        command:
        - /bin/bash
        - -c
        - |
          set -eo pipefail

          echo "Database is ready. Setting up users..."

          ./deploy/scripts/setup_database_users.sh --direct

          echo "Database users setup completed successfully!"
        volumeMounts:
        {{- include "flightctl.dbAdminSecretVolumeMount" $ctx | nindent 8 }}
        {{- include "flightctl.dbMigrationSecretVolumeMount" $ctx | nindent 8 }}
        {{- include "flightctl.dbAppSecretVolumeMount" $ctx | nindent 8 }}
      {{- end }}
      {{- end }}
      containers:
      - name: run-migrations
        image: "{{ include "flightctl.ensureOsQualifiedImage" $ctx.Values.dbSetup.image.image }}:{{ default $ctx.Chart.AppVersion $ctx.Values.dbSetup.image.tag }}"
        imagePullPolicy: {{ default $ctx.Values.global.imagePullPolicy $ctx.Values.dbSetup.image.pullPolicy }}
        env:
        - name: HOME
          value: "/root"
        {{- if eq $ctx.Values.db.type "external" }}
        {{- if $ctx.Values.db.external.sslmode }}
        - name: PGSSLMODE
          value: "{{ $ctx.Values.db.external.sslmode }}"
        {{- end }}
        {{- if $ctx.Values.db.external.tlsSecretName }}
        - name: PGSSLCERT
          value: /etc/ssl/postgres/client-cert.pem
        - name: PGSSLKEY
          value: /etc/ssl/postgres/client-key.pem
        {{- end }}
        {{- if $ctx.Values.db.external.tlsConfigMapName }}
        - name: PGSSLROOTCERT
          value: /etc/ssl/postgres/ca-cert.pem
        {{- end }}
        {{- end }}
        command:
        - /bin/bash
        - -c
        - |
          set -eo pipefail

          echo "Running database migrations..."

          # Copy config file to a writable location
          mkdir -p /tmp/.flightctl
          cp /root/.flightctl/config.yaml /tmp/.flightctl/config.yaml
          HOME=/tmp \
            /usr/local/bin/flightctl-db-migrate{{ if $isDryRun }} --dry-run{{ end }}
          echo "Migrations completed successfully!"

          {{- if not $isDryRun }}
          echo "Granting permissions on existing tables to application user..."
          {{- if eq $ctx.Values.db.type "external" }}
            APP_USER="$(cat /run/secrets/db/user)"
            PGPASSWORD="$(cat /run/secrets/db-migration/migrationPassword)" \
              psql -v ON_ERROR_STOP=1 -v app_user="$APP_USER" \
              -h {{ $ctx.Values.db.external.hostname }} -p {{ include "flightctl.dbPort" $ctx | int }} \
              -U "$(cat /run/secrets/db-migration/migrationUser)" -d "{{ (default "flightctl" $ctx.Values.db.name) }}" \
              -c 'GRANT USAGE ON SCHEMA public TO :"app_user";' \
              -c 'GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO :"app_user";' \
              -c 'GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO :"app_user";' \
              -c 'ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO :"app_user";' \
              -c 'ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO :"app_user";'
          {{- else -}}
            PGPASSWORD="$(cat /run/secrets/db-admin/masterPassword)" \
              psql -h "{{ include "flightctl.dbHostname" $ctx }}" -p {{ include "flightctl.dbPort" $ctx | int }} \
              -U "$(cat /run/secrets/db-admin/masterUser)" -d "{{ $ctx.Values.db.name }}" \
              -c "SELECT grant_app_permissions_on_existing_tables();"
          {{- end }}
          echo "Permission granting completed successfully!"
          {{- end }}
        volumeMounts:
        {{- include "flightctl.dbMigrationSecretVolumeMount" $ctx | nindent 8 }}
        {{- include "flightctl.dbAppSecretVolumeMount" $ctx | nindent 8 }}
        {{- if eq $ctx.Values.db.type "builtin" }}
        {{- include "flightctl.dbAdminSecretVolumeMount" $ctx | nindent 8 }}
        {{- end }}
        - mountPath: /root/.flightctl/
          name: flightctl-db-migration-config
          readOnly: true
        {{- include "flightctl.dbSslVolumeMounts" $ctx | nindent 8 }}
      volumes:
      {{- include "flightctl.dbMigrationSecretVolume" $ctx | nindent 6 }}
      {{- include "flightctl.dbAppSecretVolume" $ctx | nindent 6 }}
      {{- if eq $ctx.Values.db.type "builtin" }}
      {{- include "flightctl.dbAdminSecretVolume" $ctx | nindent 6 }}
      {{- end }}
      - name: flightctl-db-migration-config
        configMap:
          name: flightctl-db-migration-config
      {{- include "flightctl.dbSslVolumes" $ctx | nindent 6 }}
{{- end }}


