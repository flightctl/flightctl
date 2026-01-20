package server

//go:generate go run -modfile=../../../tools/go.mod github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config=server.gen.cfg ../../../api/core/v1beta1/openapi.yaml
//go:generate go run -modfile=../../../tools/api-metadata-extractor/go.mod ../../../tools/api-metadata-extractor/main.go "../../../api/core/*/openapi.yaml" api_metadata_registry.gen.go server
