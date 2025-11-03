# flightctl-e2e-extras

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.1.0](https://img.shields.io/badge/AppVersion-0.1.0-informational?style=flat-square)

A helm chart for flightctl E2E testing in kind

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| gitserver.image | string | `"quay.io/flightctl-tests/git-server:latest"` |  |
| global.imagePullPolicy | string | `"IfNotPresent"` |  |
| global.imagePullSecretName | string | `""` |  |
| global.internalNamespace | string | `""` |  |
| global.nodePorts.gitserver | string | `""` |  |
| global.nodePorts.jaegerUi | string | `""` |  |
| global.nodePorts.prometheus | string | `""` |  |
| global.nodePorts.registry | string | `""` |  |
| jaeger.enabled | bool | `false` |  |
| jaeger.image.image | string | `"jaegertracing/all-in-one"` |  |
| jaeger.image.pullPolicy | string | `""` |  |
| jaeger.image.tag | string | `"1.35"` |  |
| jaeger.logLevel | string | `"info"` |  |
| jaeger.maxTraces | int | `50000` |  |
| jaeger.otlpEnabled | bool | `true` |  |
| jaeger.resources.limits.cpu | string | `"200m"` |  |
| jaeger.resources.limits.memory | string | `"512Mi"` |  |
| jaeger.resources.requests.cpu | string | `"50m"` |  |
| jaeger.resources.requests.memory | string | `"128Mi"` |  |
| jaeger.storageType | string | `"memory"` |  |
| prometheus.image | string | `"quay.io/prometheus/prometheus:v2.54.1"` |  |
| registry.hostName | string | `""` |  |
| registry.image | string | `"quay.io/flightctl/e2eregistry:2"` |  |
| registry.route | bool | `true` |  |

