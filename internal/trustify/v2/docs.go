package trustifyv2

//go:generate go run -modfile=../../../tools/go.mod github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config=types.gen.cfg --include-operation-ids=analyze openapi.yaml
//go:generate go run -modfile=../../../tools/go.mod github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config=spec.gen.cfg --include-operation-ids=analyze openapi.yaml
//go:generate go run -modfile=../../../tools/go.mod github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config=client.gen.cfg --include-operation-ids=analyze openapi.yaml
