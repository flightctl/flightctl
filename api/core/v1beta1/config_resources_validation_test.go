package v1beta1

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRegistryConfigResourceTrustAndConsumers(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	for _, bundle := range [][]byte{cert, append(append([]byte{}, cert...), cert...)} {
		input := strings.ReplaceAll(registryConfigJSON, `"scheme":"https"`, `"scheme":"http","ca.crt":"`+base64.StdEncoding.EncodeToString(bundle)+`"`)
		input = strings.ReplaceAll(input, `"scope":""`, `"scope":"","authentication":{"authType":"docker","username":"user","password":"secret-sentinel"}`)
		input = strings.ReplaceAll(input, `{"type":"os"}`, `{"type":"os"},{"type":"helm-chart","user":""},{"type":"helm-image"}`)
		var resource ConfigResource
		require.NoError(t, json.Unmarshal([]byte(input), &resource))
		require.Empty(t, ValidateConfigResources([]ConfigResource{resource}))
		typed, err := resource.AsRegistryConfigResource()
		require.NoError(t, err)
		require.Empty(t, typed.Validate())
	}
	t.Run("When distinct punctuation and ports are used it should preserve identities", func(t *testing.T) {
		var resources []ConfigResource
		for _, endpoint := range []string{"registry.example.com", "registry-example.com", "registry.example.com:5000"} {
			var resource ConfigResource
			require.NoError(t, json.Unmarshal([]byte(strings.ReplaceAll(registryConfigJSON, "quay.io:5000", endpoint)), &resource))
			resources = append(resources, resource)
		}
		require.Empty(t, ValidateConfigResources(resources))
	})
}

func TestValidateConfigResources(t *testing.T) {
	tests := []struct{ name, old, replacement string }{
		{"missing name", `"name":"registry:quay.io:5000",`, ""},
		{"unknown type", `"type":"registry"`, `"type":"unknown"`},
		{"missing type", `"type":"registry",`, ""},
		{"mismatched name", `registry:quay.io:5000`, `registry:another.io`},
		{"URL endpoint", `"registry":"quay.io:5000"`, `"registry":"https://quay.io"`},
		{"uppercase endpoint", `quay.io:5000`, `QUAY.io:5000`},
		{"invalid port", `quay.io:5000`, `quay.io:70000`},
		{"invalid endpoint field type", `"registry":"quay.io:5000"`, `"registry":42`},
		{"generic content wrapper", `"scheme":"https"`, `"scheme":"https","content":"secret-sentinel"`},
		{"invalid scheme", `"scheme":"https"`, `"scheme":"ftp"`},
		{"missing boolean", `"skipServerVerification":false,`, ""},
		{"null boolean", `"skipServerVerification":false`, `"skipServerVerification":null`},
		{"empty access", `[{"scope":"","consumers":[{"type":"container","user":"workload"},{"type":"os"}]}]`, `[]`},
		{"missing scope", `"scope":"",`, ""},
		{"null scope", `"scope":""`, `"scope":null`},
		{"absolute scope", `"scope":""`, `"scope":"/team"`},
		{"traversal scope", `"scope":""`, `"scope":"team/../other"`},
		{"tagged scope", `"scope":""`, `"scope":"team:tag"`},
		{"URL scope", `"scope":""`, `"scope":"https://quay.io/team"`},
		{"empty component", `"scope":""`, `"scope":"team//other"`},
		{"unknown consumer", `"type":"container"`, `"type":"unknown"`},
		{"empty consumers", `"consumers":[{"type":"container","user":"workload"},{"type":"os"}]`, `"consumers":[]`},
		{"empty CA", `"scheme":"https"`, `"scheme":"https","ca.crt":""`},
		{"invalid CA certificate", `"scheme":"https"`, `"scheme":"https","ca.crt":"` + base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("secret-sentinel")})) + `"`},
		{"CA with PEM headers", `"scheme":"https"`, `"scheme":"https","ca.crt":"` + base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"Comment": "secret-sentinel"}, Bytes: []byte("secret-sentinel")})) + `"`},
		{"invalid CA base64", `"scheme":"https"`, `"scheme":"https","ca.crt":"secret-sentinel"`},
		{"non PEM CA", `"scheme":"https"`, `"scheme":"https","ca.crt":"` + base64.StdEncoding.EncodeToString([]byte("secret-sentinel")) + `"`},
		{"empty auth", `"scope":""`, `"scope":"","authentication":{"authType":"docker","username":"","password":"secret-sentinel"}`},
		{"empty password", `"scope":""`, `"scope":"","authentication":{"authType":"docker","username":"secret-sentinel","password":""}`},
		{"unknown auth", `"scope":""`, `"scope":"","authentication":{"authType":"unknown","username":"user","password":"secret-sentinel"}`},
	}
	for _, tt := range tests {
		t.Run("When "+tt.name+" it should reject without exposing secrets", func(t *testing.T) {
			var resource ConfigResource
			require.NoError(t, json.Unmarshal([]byte(strings.ReplaceAll(registryConfigJSON, tt.old, tt.replacement)), &resource))
			errs := ValidateConfigResources([]ConfigResource{resource})
			require.NotEmpty(t, errs)
			require.NotContains(t, errors.Join(errs...).Error(), "secret-sentinel")
		})
	}
	t.Run("When a typed registry resource has an unknown kind it should reject", func(t *testing.T) {
		var registry RegistryConfigResource
		require.NoError(t, json.Unmarshal([]byte(registryConfigJSON), &registry))
		registry.Type = "unknown"
		require.ErrorContains(t, errors.Join(registry.Validate()...), "type must be registry")
	})
	t.Run("When content is valid it should accept scoped and registry wide access", func(t *testing.T) {
		for _, scope := range []string{"", "team/project", "team-a/project_name", "team.example/project"} {
			var resource ConfigResource
			require.NoError(t, json.Unmarshal([]byte(strings.ReplaceAll(registryConfigJSON, `"scope":""`, `"scope":"`+scope+`"`)), &resource))
			require.Empty(t, ValidateConfigResources([]ConfigResource{resource}))
			require.NotEmpty(t, ValidateConfigResources([]ConfigResource{resource, resource}))
		}
		require.Empty(t, ValidateConfigResources(nil))
		require.Empty(t, ValidateConfigResources([]ConfigResource{}))
	})
}
