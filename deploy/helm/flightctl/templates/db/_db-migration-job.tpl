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
    app: flightctl-db-migration
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
        app: flightctl-db-migration
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
        image: "{{ $ctx.Values.dbSetup.image.image }}:{{ default $ctx.Chart.AppVersion $ctx.Values.dbSetup.image.tag }}"
        imagePullPolicy: {{ default $ctx.Values.global.imagePullPolicy $ctx.Values.dbSetup.image.pullPolicy }}
        env:
        - name: DB_HOST
          value: "{{ include "flightctl.dbHostname" $ctx }}"
        - name: DB_PORT
          value: "{{ include "flightctl.dbPort" $ctx }}"
        - name: DB_NAME
          value: "{{ $ctx.Values.db.name }}"
        {{- if eq $ctx.Values.db.type "builtin" }}
        - name: DB_ADMIN_USER
          valueFrom:
            secretKeyRef:
              name: {{ default "flightctl-db-admin-secret" $ctx.Values.db.builtin.masterUserSecretName }}
              key: masterUser
        - name: DB_ADMIN_PASSWORD
          valueFrom:
            secretKeyRef:
              name: {{ default "flightctl-db-admin-secret" $ctx.Values.db.builtin.masterUserSecretName }}
              key: masterPassword
        {{- end }}
        - name: DB_MIGRATION_USER
          valueFrom:
            secretKeyRef:
              name: {{ include "flightctl.dbMigrationUserSecret" $ctx }}
              key: migrationUser
        - name: DB_MIGRATION_PASSWORD
          valueFrom:
            secretKeyRef:
              name: {{ include "flightctl.dbMigrationUserSecret" $ctx }}
              key: migrationPassword
        - name: DB_APP_USER
          valueFrom:
            secretKeyRef:
              name: {{ include "flightctl.dbAppUserSecret" $ctx }}
              key: user
        - name: DB_APP_PASSWORD
          valueFrom:
            secretKeyRef:
              name: {{ include "flightctl.dbAppUserSecret" $ctx }}
              key: userPassword
        command:
        - /bin/bash
        - -c
        - |
          set -eo pipefail

          echo "Database is ready. Setting up users..."

          # Create temporary SQL file with environment variable substitution
          export DB_HOST DB_PORT DB_NAME DB_ADMIN_USER DB_ADMIN_PASSWORD
          export DB_MIGRATION_USER DB_MIGRATION_PASSWORD DB_APP_USER DB_APP_PASSWORD

          SQL_FILE="/tmp/setup_database_users.sql"
          envsubst < ./deploy/scripts/setup_database_users.sql > "$SQL_FILE"

          # Execute the SQL file
          echo "Running database user setup SQL..."
          PGPASSWORD="$DB_ADMIN_PASSWORD" psql -v ON_ERROR_STOP=1 -h "$DB_HOST" -p "$DB_PORT" -U "$DB_ADMIN_USER" -d "$DB_NAME" -f "$SQL_FILE"

          # Clean up temporary file
          rm -f "$SQL_FILE"

          echo "Database users setup completed successfully!"
      {{- end }}
      {{- end }}
      containers:
      - name: run-migrations
        image: "{{ $ctx.Values.dbSetup.image.image }}:{{ default $ctx.Chart.AppVersion $ctx.Values.dbSetup.image.tag }}"
        imagePullPolicy: {{ default $ctx.Values.global.imagePullPolicy $ctx.Values.dbSetup.image.pullPolicy }}
        env:
        - name: HOME
          value: "/root"
        - name: DB_USER
          valueFrom:
            secretKeyRef:
              name: {{ include "flightctl.dbMigrationUserSecret" $ctx }}
              key: migrationUser
        - name: DB_PASSWORD
          valueFrom:
            secretKeyRef:
              name: {{ include "flightctl.dbMigrationUserSecret" $ctx }}
              key: migrationPassword
        - name: DB_MIGRATION_USER
          valueFrom:
            secretKeyRef:
              name: {{ include "flightctl.dbMigrationUserSecret" $ctx }}
              key: migrationUser
        - name: DB_MIGRATION_PASSWORD
          valueFrom:
            secretKeyRef:
              name: {{ include "flightctl.dbMigrationUserSecret" $ctx }}
              key: migrationPassword
        - name: DB_APP_USER
          valueFrom:
            secretKeyRef:
              name: {{ include "flightctl.dbAppUserSecret" $ctx }}
              key: user
        - name: DB_APP_PASSWORD
          valueFrom:
            secretKeyRef:
              name: {{ include "flightctl.dbAppUserSecret" $ctx }}
              key: userPassword
        {{- if eq $ctx.Values.db.type "builtin" }}
        - name: DB_ADMIN_USER
          valueFrom:
            secretKeyRef:
              name: {{ default "flightctl-db-admin-secret" $ctx.Values.db.builtin.masterUserSecretName }}
              key: masterUser
        - name: DB_ADMIN_PASSWORD
          valueFrom:
            secretKeyRef:
              name: {{ default "flightctl-db-admin-secret" $ctx.Values.db.builtin.masterUserSecretName }}
              key: masterPassword
        {{- end }}
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
          export HOME=/tmp

          /usr/local/bin/flightctl-db-migrate{{ if $isDryRun }} --dry-run{{ end }}
          echo "Migrations completed successfully!"

          {{- if not $isDryRun }}
          # Grant permissions on all existing tables to the application user
          echo "Granting permissions on existing tables to application user..."
          {{- if eq $ctx.Values.db.type "external" }}
            export PGPASSWORD="$DB_MIGRATION_PASSWORD"
            psql -v ON_ERROR_STOP=1 -h {{ $ctx.Values.db.external.hostname }} -p {{ include "flightctl.dbPort" $ctx | int }} -U "$DB_MIGRATION_USER" -d "{{ (default "flightctl" $ctx.Values.db.name) }}" -c "GRANT USAGE ON SCHEMA public TO \"${DB_APP_USER}\";"
            psql -v ON_ERROR_STOP=1 -h {{ $ctx.Values.db.external.hostname }} -p {{ include "flightctl.dbPort" $ctx | int }} -U "$DB_MIGRATION_USER" -d "{{ (default "flightctl" $ctx.Values.db.name) }}" -c "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO \"${DB_APP_USER}\";"
            psql -v ON_ERROR_STOP=1 -h {{ $ctx.Values.db.external.hostname }} -p {{ include "flightctl.dbPort" $ctx | int }} -U "$DB_MIGRATION_USER" -d "{{ (default "flightctl" $ctx.Values.db.name) }}" -c "GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO \"${DB_APP_USER}\";"
            # Optional but recommended: ensure future objects carry privileges
            psql -v ON_ERROR_STOP=1 -h {{ $ctx.Values.db.external.hostname }} -p {{ include "flightctl.dbPort" $ctx | int }} -U "$DB_MIGRATION_USER" -d "{{ (default "flightctl" $ctx.Values.db.name) }}" -c "ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO \"${DB_APP_USER}\";"
            psql -v ON_ERROR_STOP=1 -h {{ $ctx.Values.db.external.hostname }} -p {{ include "flightctl.dbPort" $ctx | int }} -U "$DB_MIGRATION_USER" -d "{{ (default "flightctl" $ctx.Values.db.name) }}" -c "ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO \"${DB_APP_USER}\";"
          {{- else -}}
            # Need to get admin credentials from init container environment
            DB_HOST="{{ include "flightctl.dbHostname" $ctx }}"
            # Get admin credentials from the same secrets used by init container
            export PGPASSWORD="$DB_ADMIN_PASSWORD"
            psql -h "$DB_HOST" -p {{ include "flightctl.dbPort" $ctx | int }} -U "$DB_ADMIN_USER" -d "{{ $ctx.Values.db.name }}" -c "SELECT grant_app_permissions_on_existing_tables();"
          {{- end }}
          echo "Permission granting completed successfully!"
          {{- end }}
        volumeMounts:
        - mountPath: /root/.flightctl/
          name: flightctl-db-migration-config
          readOnly: true
        {{- include "flightctl.dbSslVolumeMounts" $ctx | nindent 8 }}
      volumes:
      - name: flightctl-db-migration-config
        configMap:
          name: flightctl-db-migration-config
      {{- include "flightctl.dbSslVolumes" $ctx | nindent 6 }}
{{- end }}


