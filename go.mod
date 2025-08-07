module github.com/flightctl/flightctl

go 1.23.0

toolchain go1.23.9

require (
	github.com/ccoveille/go-safecast v1.1.0
	github.com/containers/image/v5 v5.30.1
	github.com/coreos/ignition/v2 v2.19.0
	github.com/coreos/rpmostree-client-go v0.0.0-20240514234259-72a33e8554b6
	github.com/creack/pty v1.1.24
	github.com/dustin/go-humanize v1.0.1
	github.com/evanphx/json-patch v5.9.0+incompatible
	github.com/getkin/kin-openapi v0.131.0
	github.com/gliderlabs/ssh v0.3.8
	github.com/go-chi/chi/v5 v5.2.2
	github.com/go-chi/httprate v0.15.0
	github.com/go-git/go-billy/v5 v5.6.0
	github.com/go-git/go-git/v5 v5.13.0
	github.com/google/go-cmp v0.7.0
	github.com/google/go-tpm v0.9.5
	github.com/google/go-tpm-tools v0.4.5
	github.com/google/renameio v1.0.1
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/grpc-ecosystem/go-grpc-middleware/v2 v2.1.0
	github.com/jellydator/ttlcache/v3 v3.3.0
	github.com/lestrrat-go/jwx/v2 v2.1.0
	github.com/mackerelio/go-osstat v0.2.5
	github.com/oapi-codegen/nethttp-middleware v1.0.1
	github.com/oapi-codegen/runtime v1.1.1
	github.com/onsi/ginkgo/v2 v2.19.0
	github.com/onsi/gomega v1.34.1
	github.com/openshift/library-go v0.0.0-20231130204458-653f82d961a1
	github.com/openshift/osincli v0.0.0-20160924135400-fababb0555f2
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c
	github.com/prometheus/client_golang v1.19.0
	github.com/redis/go-redis/extra/redisotel/v9 v9.7.3
	github.com/redis/go-redis/v9 v9.7.3
	github.com/robfig/cron/v3 v3.0.1
	github.com/samber/lo v1.49.1
	github.com/secure-systems-lab/go-securesystemslib v0.8.0
	github.com/sirupsen/logrus v1.9.3
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/spf13/cobra v1.8.0
	github.com/spf13/pflag v1.0.6-0.20210604193023-d5e0c0615ace
	github.com/stoewer/go-strcase v1.3.0
	github.com/stretchr/testify v1.10.0
	github.com/vincent-petithory/dataurl v1.0.0
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.60.0
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.60.0
	go.opentelemetry.io/otel v1.35.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.35.0
	go.opentelemetry.io/otel/sdk v1.35.0
	go.opentelemetry.io/otel/trace v1.35.0
	go.uber.org/mock v0.5.1
	golang.org/x/crypto v0.36.0
	golang.org/x/exp v0.0.0-20240719175910-8a7402abbf56
	golang.org/x/sync v0.12.0
	golang.org/x/sys v0.31.0
	golang.org/x/term v0.30.0
	google.golang.org/grpc v1.71.0
	google.golang.org/protobuf v1.36.5
	gopkg.in/yaml.v3 v3.0.1
	gorm.io/driver/postgres v1.5.9
	gorm.io/driver/sqlite v1.5.5
	gorm.io/gorm v1.25.10
	gorm.io/plugin/opentelemetry v0.1.12
	gorm.io/plugin/prometheus v0.1.0
	k8s.io/api v0.31.1
	k8s.io/apimachinery v0.31.1
	k8s.io/client-go v1.5.2
	k8s.io/klog/v2 v2.130.1
	k8s.io/kubectl v0.0.0-00010101000000-000000000000
	libvirt.org/go/libvirt v1.10003.0
	sigs.k8s.io/yaml v1.4.0
)

require (
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.26.1 // indirect
	github.com/redis/go-redis/extra/rediscmd/v9 v9.7.3 // indirect
	go.opentelemetry.io/auto/sdk v1.1.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.35.0 // indirect
	go.opentelemetry.io/otel/metric v1.35.0 // indirect
	go.opentelemetry.io/proto/otlp v1.5.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20250218202821-56aae31c358a // indirect
)

require (
	dario.cat/mergo v1.0.0 // indirect
	github.com/Azure/go-ansiterm v0.0.0-20210617225240-d185dfc1b5a1 // indirect
	github.com/Microsoft/go-winio v0.6.1 // indirect
	github.com/ProtonMail/go-crypto v1.1.3 // indirect
	github.com/anmitsu/go-shlex v0.0.0-20200514113438-38f4b401e2be // indirect
	github.com/apapsch/go-jsonmerge/v2 v2.0.0 // indirect
	github.com/aws/aws-sdk-go v1.53.5 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cloudflare/circl v1.3.7 // indirect
	github.com/containers/storage v1.53.0 // indirect
	github.com/coreos/go-json v0.0.0-20230131223807-18775e0fb4fb // indirect
	github.com/coreos/go-semver v0.3.1 // indirect
	github.com/coreos/go-systemd/v22 v22.5.0 // indirect
	github.com/coreos/vcontext v0.0.0-20230201181013-d72178a18687 // indirect
	github.com/cyphar/filepath-securejoin v0.2.5 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/emicklei/go-restful/v3 v3.11.0 // indirect
	github.com/emirpasic/gods v1.18.1 // indirect
	github.com/go-git/gcfg v1.5.1-0.20230307220236-3a3c6141e376 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-openapi/jsonpointer v0.21.0 // indirect
	github.com/go-openapi/jsonreference v0.20.2 // indirect
	github.com/go-openapi/swag v0.23.0 // indirect
	github.com/go-task/slim-sprig/v3 v3.0.0 // indirect
	github.com/go-test/deep v1.1.0 // indirect
	github.com/goccy/go-json v0.10.3 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/gnostic-models v0.6.9 // indirect
	github.com/google/go-configfs-tsm v0.3.3-0.20240919001351-b4b5b84fdcbc // indirect
	github.com/google/go-sev-guest v0.12.1 // indirect
	github.com/google/go-tdx-guest v0.3.2-0.20241009005452-097ee70d0843 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/google/logger v1.1.1 // indirect
	github.com/google/pprof v0.0.0-20240727154555-813a5fbdbec8 // indirect
	github.com/gorilla/mux v1.8.1 // indirect
	github.com/imdario/mergo v0.3.7 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20221227161230-091c0ba34f0a // indirect
	github.com/jackc/pgx/v5 v5.5.5 // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	github.com/jbenet/go-context v0.0.0-20150711004518-d14ea06fba99 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/kevinburke/ssh_config v1.2.0 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	github.com/lestrrat-go/blackmagic v1.0.2 // indirect
	github.com/lestrrat-go/httpcc v1.0.1 // indirect
	github.com/lestrrat-go/httprc v1.0.5 // indirect
	github.com/lestrrat-go/iter v1.0.2 // indirect
	github.com/lestrrat-go/option v1.0.1 // indirect
	github.com/liggitt/tabwriter v0.0.0-20181228230101-89fcab3d43de // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/mattn/go-sqlite3 v1.14.22 // indirect
	github.com/mitchellh/go-wordwrap v1.0.1 // indirect
	github.com/moby/spdystream v0.4.0 // indirect
	github.com/moby/term v0.5.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/mohae/deepcopy v0.0.0-20170929034955-c48cc78d4826 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/mxk/go-flowrate v0.0.0-20140419014527-cca7078d478f // indirect
	github.com/oasdiff/yaml v0.0.0-20250309154309-f31be36b4037 // indirect
	github.com/oasdiff/yaml3 v0.0.0-20250309153720-d2182401db90 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/perimeterx/marshmallow v1.1.5 // indirect
	github.com/pjbgf/sha1cd v0.3.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/prometheus/client_model v0.5.0 // indirect
	github.com/prometheus/common v0.48.0 // indirect
	github.com/prometheus/procfs v0.12.0 // indirect
	github.com/segmentio/asm v1.2.0 // indirect
	github.com/sergi/go-diff v1.3.2-0.20230802210424-5b0b94c5c0d3 // indirect
	github.com/skeema/knownhosts v1.3.0 // indirect
	github.com/xanzy/ssh-agent v0.3.3 // indirect
	github.com/zeebo/xxh3 v1.0.2 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/mod v0.20.0 // indirect
	golang.org/x/net v0.38.0 // indirect
	golang.org/x/oauth2 v0.27.0 // indirect
	golang.org/x/text v0.23.0 // indirect
	golang.org/x/time v0.5.0 // indirect
	golang.org/x/tools v0.24.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250218202821-56aae31c358a // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/warnings.v0 v0.1.2 // indirect
	k8s.io/apiserver v0.28.2 // indirect
	k8s.io/cli-runtime v0.29.15 // indirect
	k8s.io/kube-openapi v0.29.15 // indirect
	k8s.io/utils v0.0.0-20240711033017-18e509b52bc8 // indirect
	sigs.k8s.io/json v0.0.0-20221116044647-bc3834ca7abd // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.6.0 // indirect
)

replace (
	dario.cat/mergo => github.com/imdario/mergo v1.0.0
	github.com/google/gnostic => github.com/google/gnostic v0.5.7-v3refs
	github.com/imdario/mergo => github.com/imdario/mergo v0.3.16
	k8s.io/api => k8s.io/api v0.29.15
	k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.29.15
	k8s.io/apimachinery => k8s.io/apimachinery v0.29.15
	k8s.io/apiserver => k8s.io/apiserver v0.29.15
	k8s.io/cli-runtime => k8s.io/cli-runtime v0.29.15
	k8s.io/client-go => k8s.io/client-go v0.29.15
	k8s.io/cloud-provider => k8s.io/cloud-provider v0.29.15
	k8s.io/cluster-bootstrap => k8s.io/cluster-bootstrap v0.29.15
	k8s.io/code-generator => k8s.io/code-generator v0.29.15
	k8s.io/component-base => k8s.io/component-base v0.29.15
	k8s.io/component-helpers => k8s.io/component-helpers v0.29.15
	k8s.io/controller-manager => k8s.io/controller-manager v0.29.15
	k8s.io/cri-api => k8s.io/cri-api v0.29.15
	k8s.io/csi-translation-lib => k8s.io/csi-translation-lib v0.29.15
	k8s.io/kube-aggregator => k8s.io/kube-aggregator v0.29.15
	k8s.io/kube-controller-manager => k8s.io/kube-controller-manager v0.29.15
	k8s.io/kube-openapi => k8s.io/kube-openapi v0.0.0-20250318190949-c8a335a9a2ff
	k8s.io/kube-proxy => k8s.io/kube-proxy v0.29.15
	k8s.io/kube-scheduler => k8s.io/kube-scheduler v0.29.15
	k8s.io/kubectl => k8s.io/kubectl v0.29.15
	k8s.io/kubelet => k8s.io/kubelet v0.29.15
	k8s.io/legacy-cloud-providers => k8s.io/legacy-cloud-providers v0.29.15
	k8s.io/metrics => k8s.io/metrics v0.29.15
	k8s.io/mount-utils => k8s.io/mount-utils v0.29.15
	k8s.io/pod-security-admission => k8s.io/pod-security-admission v0.29.15
	k8s.io/sample-apiserver => k8s.io/sample-apiserver v0.29.15
)
