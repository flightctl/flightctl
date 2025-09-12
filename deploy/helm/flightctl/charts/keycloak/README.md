# keycloak

![Version: 0.0.1](https://img.shields.io/badge/Version-0.0.1-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.0.1](https://img.shields.io/badge/AppVersion-0.0.1-informational?style=flat-square)

A helm chart for keycloak

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| db.fsGroup | string | `""` |  |
| db.image | string | `"quay.io/sclorg/postgresql-16-c9s"` |  |
| db.imagePullPolicy | string | `""` |  |
| db.tag | string | `"20250214"` |  |
| directAccessGrantsEnabled | bool | `true` |  |
| global.imagePullPolicy | string | `""` |  |
| global.storageClassName | string | `""` |  |
| image | string | `"quay.io/keycloak/keycloak:26.2.5"` |  |
| imagePullPolicy | string | `""` |  |
| realm.redirectUris | string | `""` |  |
| realm.webOrigins | string | `""` |  |
| service.nodePorts.http | string | `""` |  |
| service.nodePorts.https | string | `""` |  |

