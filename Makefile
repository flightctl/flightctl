.PHONY: all
all: install-oapi-codegen oapi-codegen-all

.PHONY: install-oapi-codegen
install-oapi-codegen:
	go install github.com/deepmap/oapi-codegen/cmd/oapi-codegen@v1.15.0

.PHONY: oapi-codegen-all
oapi-codegen-all: oapi-codegen-types oapi-codegen-spec oapi-codegen-server oapi-codegen-client

.PHONY: oapi-codegen-types
oapi-codegen-types:
	oapi-codegen -config api/v1alpha1/oapi-codegen-configs/types.yaml api/v1alpha1/openapi.yaml

.PHONY: oapi-codegen-spec
oapi-codegen-spec:
	oapi-codegen -config api/v1alpha1/oapi-codegen-configs/spec.yaml api/v1alpha1/openapi.yaml

.PHONY: oapi-codegen-server
oapi-codegen-server:
	oapi-codegen -config api/v1alpha1/oapi-codegen-configs/server.yaml api/v1alpha1/openapi.yaml

.PHONY: oapi-codegen-client
oapi-codegen-client:
	oapi-codegen -config api/v1alpha1/oapi-codegen-configs/client.yaml api/v1alpha1/openapi.yaml
