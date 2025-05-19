{{/*
Looks up a password from an existing Secret, otherwise returns a randomly generated password.

Usage:
{{ include "lookupOrGeneratePassword" (dict "secret" "secretName" "namespace" "secretNamespace" "key" "keyName" "context" $) }}

Params:
  - secret - String - Required - Name of the 'Secret' resource where the password is stored.
  - namespace - String - Required - Namespace of the 'Secret' resource where the password is stored.
  - key - String - Required - Name of the key in the secret.
  - context - Context - Required - Parent context.
*/}}
{{- define "keycloak.lookupOrGeneratePassword" -}}
{{- $password := "" }}
{{- $namespace := (default .context.Release.Namespace .namespace | trunc 63 | trimSuffix "-") }}
{{- $secretData := (lookup "v1" "Secret" $namespace .secret).data }}
{{- if $secretData }}
  {{- if hasKey $secretData .key }}
    {{- $password = index $secretData .key }}
  {{- else -}}
    {{- printf "\nERROR: The secret \"%s\" does not contain the key \"%s\"\n" .secret .key | fail -}}
  {{- end -}}
{{- else -}}
  {{- $password = (include "flightctl.generatePassword" .context) }}
{{- end -}}
{{- printf "%s" $password -}}
{{- end -}}
