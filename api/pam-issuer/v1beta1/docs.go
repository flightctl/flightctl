// Package pam_issuer provides primitives to interact with the PAM Issuer openapi HTTP API.
package pam_issuer

//go:generate go run -modfile=../../../tools/go.mod github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config=types.gen.cfg openapi.yaml
//go:generate go run -modfile=../../../tools/go.mod github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config=spec.gen.cfg openapi.yaml
