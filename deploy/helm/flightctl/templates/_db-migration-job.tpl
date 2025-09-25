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
{{- $deletePolicy := .hookDeletePolicy | default "hook-succeeded" }}
{{- $isDryRun := .isDryRun | default false }}
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ $name }}
  namespace: {{ default $ctx.Release.Namespace $ctx.Values.global.internalNamespace }}
  labels:
    app: flightctl-db-migration
    release: {{ $ctx.Release.Name }}
  annotations:
    {{- if gt (len $hooks) 0 }}
    helm.sh/hook: {{ join "," $hooks }}
    helm.sh/hook-weight: "{{ $hookWeight }}"
    helm.sh/hook-delete-policy: "{{ $deletePolicy }}"
    {{- end }}
spec:
  backoffLimit: {{ $ctx.Values.dbSetup.migration.backoffLimit | int }}
  activeDeadlineSeconds: {{ $ctx.Values.dbSetup.migration.activeDeadlineSeconds | int }}
  template:
    metadata:
      labels:
        app: flightctl-db-migration
        release: {{ $ctx.Release.Name }}
    spec:
      restartPolicy: Never
      serviceAccountName: flightctl-db-migration
      initContainers:
      {{- $userType := ternary "admin" "migration" (ne $ctx.Values.db.external "enabled") }}
      {{- include "flightctl.databaseWaitInitContainer" (dict "context" $ctx "userType" $userType "timeout" 120 "sleep" 2) | nindent 6 }}
      {{- if ne $ctx.Values.db.external "enabled" }}
      {{- if not $isDryRun }}
      - name: setup-database-users
        image: "{{ $ctx.Values.dbSetup.image.image }}:{{ default $ctx.Chart.AppVersion $ctx.Values.dbSetup.image.tag }}"
        imagePullPolicy: {{ default $ctx.Values.global.imagePullPolicy $ctx.Values.dbSetup.image.pullPolicy }}
        env:
        - name: DB_HOST
          value: "{{ include "flightctl.dbHostname" $ctx }}"
        - name: DB_PORT
          value: "{{ $ctx.Values.db.port }}"
        - name: DB_NAME
          value: "{{ $ctx.Values.db.name }}"
        - name: DB_ADMIN_USER
          valueFrom:
            secretKeyRef:
              name: flightctl-db-admin-secret
              key: masterUser
        - name: DB_ADMIN_PASSWORD
          valueFrom:
            secretKeyRef:
              name: flightctl-db-admin-secret
              key: masterPassword
        - name: DB_MIGRATION_USER
          valueFrom:
            secretKeyRef:
              name: flightctl-db-migration-secret
              key: migrationUser
        - name: DB_MIGRATION_PASSWORD
          valueFrom:
            secretKeyRef:
              name: flightctl-db-migration-secret
              key: migrationPassword
        - name: DB_APP_USER
          valueFrom:
            secretKeyRef:
              name: flightctl-db-app-secret
              key: user
        - name: DB_APP_PASSWORD
          valueFrom:
            secretKeyRef:
              name: flightctl-db-app-secret
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
              name: flightctl-db-migration-secret
              key: migrationUser
        - name: DB_PASSWORD
          valueFrom:
            secretKeyRef:
              name: flightctl-db-migration-secret
              key: migrationPassword
        - name: DB_MIGRATION_USER
          valueFrom:
            secretKeyRef:
              name: flightctl-db-migration-secret
              key: migrationUser
        - name: DB_MIGRATION_PASSWORD
          valueFrom:
            secretKeyRef:
              name: flightctl-db-migration-secret
              key: migrationPassword
        - name: DB_APP_USER
          valueFrom:
            secretKeyRef:
              name: flightctl-db-app-secret
              key: user
        - name: DB_APP_PASSWORD
          valueFrom:
            secretKeyRef:
              name: flightctl-db-app-secret
              key: userPassword
        - name: DB_ADMIN_USER
          valueFrom:
            secretKeyRef:
              name: flightctl-db-admin-secret
              key: masterUser
        - name: DB_ADMIN_PASSWORD
          valueFrom:
            secretKeyRef:
              name: flightctl-db-admin-secret
              key: masterPassword
        {{- if $ctx.Values.db.sslmode }}
        - name: PGSSLMODE
          value: "{{ $ctx.Values.db.sslmode }}"
        {{- end }}
        {{- if $ctx.Values.db.sslcert }}
        - name: PGSSLCERT
          value: "{{ $ctx.Values.db.sslcert }}"
        {{- end }}
        {{- if $ctx.Values.db.sslkey }}
        - name: PGSSLKEY
          value: "{{ $ctx.Values.db.sslkey }}"
        {{- end }}
        {{- if $ctx.Values.db.sslrootcert }}
        - name: PGSSLROOTCERT
          value: "{{ $ctx.Values.db.sslrootcert }}"
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
          {{- if eq $ctx.Values.db.external "enabled" }}
            export PGPASSWORD="$DB_MIGRATION_PASSWORD"
            psql -v ON_ERROR_STOP=1 -h {{ $ctx.Values.db.hostname }} -p {{ (default 5432 $ctx.Values.db.port) }} -U "$DB_MIGRATION_USER" -d "{{ (default "flightctl" $ctx.Values.db.name) }}" -c "GRANT USAGE ON SCHEMA public TO \"${DB_APP_USER}\";"
            psql -v ON_ERROR_STOP=1 -h {{ $ctx.Values.db.hostname }} -p {{ (default 5432 $ctx.Values.db.port) }} -U "$DB_MIGRATION_USER" -d "{{ (default "flightctl" $ctx.Values.db.name) }}" -c "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO \"${DB_APP_USER}\";"
            psql -v ON_ERROR_STOP=1 -h {{ $ctx.Values.db.hostname }} -p {{ (default 5432 $ctx.Values.db.port) }} -U "$DB_MIGRATION_USER" -d "{{ (default "flightctl" $ctx.Values.db.name) }}" -c "GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO \"${DB_APP_USER}\";"
            # Optional but recommended: ensure future objects carry privileges
            psql -v ON_ERROR_STOP=1 -h {{ $ctx.Values.db.hostname }} -p {{ (default 5432 $ctx.Values.db.port) }} -U "$DB_MIGRATION_USER" -d "{{ (default "flightctl" $ctx.Values.db.name) }}" -c "ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO \"${DB_APP_USER}\";"
            psql -v ON_ERROR_STOP=1 -h {{ $ctx.Values.db.hostname }} -p {{ (default 5432 $ctx.Values.db.port) }} -U "$DB_MIGRATION_USER" -d "{{ (default "flightctl" $ctx.Values.db.name) }}" -c "ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO \"${DB_APP_USER}\";"
          {{- else -}}
            # Need to get admin credentials from init container environment
            DB_HOST="{{ include "flightctl.dbHostname" $ctx }}"
            # Get admin credentials from the same secrets used by init container
            export PGPASSWORD="$DB_ADMIN_PASSWORD"
            psql -h "$DB_HOST" -p {{ $ctx.Values.db.port }} -U "$DB_ADMIN_USER" -d "{{ $ctx.Values.db.name }}" -c "SELECT grant_app_permissions_on_existing_tables();"
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


