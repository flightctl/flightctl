# ui

![Version: 0.0.1](https://img.shields.io/badge/Version-0.0.1-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: latest](https://img.shields.io/badge/AppVersion-latest-informational?style=flat-square)

A helm chart for flightctl UI

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| alerts.enabled | bool | `true` |  |
| api.caCert | string | `""` |  |
| api.insecureSkipTlsVerify | bool | `false` |  |
| api.url | string | `"https://flightctl-api:3443/"` |  |
| auth.caCert | string | `""` |  |
| auth.clientId | string | `"flightctl"` |  |
| auth.insecureSkipTlsVerify | bool | `false` |  |
| auth.internalAuthUrl | string | `""` |  |
| baseURL | string | `""` |  |
| cliArtifacts.enabled | bool | `true` |  |
| image.image | string | `""` |  |
| image.pullPolicy | string | `""` |  |
| image.tag | string | `""` |  |
| isRHEM | bool | `false` |  |

