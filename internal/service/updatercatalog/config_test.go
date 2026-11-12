package updatercatalog_test

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/service/updatercatalog"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func digest(data string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(data))) }
func fixture(t *testing.T) map[string]any {
	t.Helper()
	key, err := x509.MarshalPKIXPublicKey(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public())
	require.NoError(t, err)
	policy := map[string]any{"version": 1, "type": "sigstoreSigned", "keyData": base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: key})), "signedIdentity": map[string]any{"type": "exactRepository", "dockerRepository": "quay.io/example/updater"}}
	policyBytes, err := json.Marshal(policy)
	require.NoError(t, err)
	schemas := map[string]any{}
	for _, name := range []string{"config", "observation", "request", "result", "checkpoint"} {
		raw := `{"type":"object","additionalProperties":false,"properties":{}}`
		if name == "observation" {
			raw = `{"type":"object","additionalProperties":false,"properties":{"active":{"type":"boolean"}}}`
		}
		schemas[name] = map[string]any{"digest": digest(raw), "bytes": raw}
	}
	limits := map[string]any{}
	for _, k := range []string{"maxRequestBytes", "maxResultBytes", "maxStderrBytes", "maxCheckpointBytes", "maxScratchBytes", "maxMemoryBytes", "maxProcesses", "maxInvocationSeconds", "maxCycles", "maxReboots", "maxAttempts"} {
		limits[k] = int64(1024)
	}
	return map[string]any{"apiVersion": "firmware-catalog/v1", "revision": 1, "releases": []any{map[string]any{"name": "orin", "version": "1.0.0", "packageDigest": digest("image"), "publisher": map[string]any{"identity": "example", "policyDigest": digest(string(policyBytes)), "policy": policy}, "protocol": 1, "schemas": schemas, "dependencies": []any{map[string]any{"name": "openssl", "constraint": ">=3"}}, "access": map[string]any{"prepare": map[string]any{"networkRepositories": []string{"artifacts"}, "credentialRepositories": []string{"artifacts"}}, "apply": map[string]any{"writePaths": []string{"/boot/efi/EFI/UpdateCapsule"}}, "verify": map[string]any{"readPaths": []string{"/sys/firmware/efi/esrt"}}}, "limits": limits, "budgetKeySet": []string{"maxCycles", "maxAttempts"}}}}
}
func release(c map[string]any) map[string]any { return c["releases"].([]any)[0].(map[string]any) }
func encode(t *testing.T, c map[string]any) []byte {
	t.Helper()
	b, e := json.Marshal(c)
	require.NoError(t, e)
	return b
}
func repolicy(t *testing.T, r map[string]any, mutate func(map[string]any)) {
	t.Helper()
	p := r["publisher"].(map[string]any)
	mutate(p["policy"].(map[string]any))
	b, e := json.Marshal(p["policy"])
	require.NoError(t, e)
	p["policyDigest"] = digest(string(b))
}

func TestParseAdministratorConfig(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unknown field", func(c map[string]any) { c["command"] = "shell" }},
		{"unsupported version", func(c map[string]any) { c["apiVersion"] = "v2" }},
		{"zero revision", func(c map[string]any) { c["revision"] = 0 }},
		{"negative revision", func(c map[string]any) { c["revision"] = -1 }},
		{"unknown protocol", func(c map[string]any) { release(c)["protocol"] = 2 }},
		{"missing publisher", func(c map[string]any) { delete(release(c), "publisher") }},
		{"missing name", func(c map[string]any) { release(c)["name"] = "" }},
		{"bad package digest", func(c map[string]any) { release(c)["packageDigest"] = "latest" }},
		{"duplicate identity", func(c map[string]any) { c["releases"] = append(c["releases"].([]any), release(c)) }},
		{"missing limits", func(c map[string]any) { delete(release(c), "limits") }},
		{"unbounded invocation", func(c map[string]any) { release(c)["limits"].(map[string]any)["maxInvocationSeconds"] = 0 }},
		{"negative limit", func(c map[string]any) { release(c)["limits"].(map[string]any)["maxScratchBytes"] = -1 }},
		{"missing access", func(c map[string]any) { delete(release(c), "access") }},
		{"apply downloads", func(c map[string]any) {
			release(c)["access"].(map[string]any)["apply"].(map[string]any)["networkRepositories"] = []string{"artifacts"}
		}},
		{"verify writes", func(c map[string]any) {
			release(c)["access"].(map[string]any)["verify"].(map[string]any)["writePaths"] = []string{"/boot"}
		}},
		{"verify credentials", func(c map[string]any) {
			release(c)["access"].(map[string]any)["verify"].(map[string]any)["credentialRepositories"] = []string{"artifacts"}
		}},
		{"prepare boot writes", func(c map[string]any) {
			release(c)["access"].(map[string]any)["prepare"].(map[string]any)["writePaths"] = []string{"/boot"}
		}},
		{"root write", func(c map[string]any) {
			release(c)["access"].(map[string]any)["apply"].(map[string]any)["writePaths"] = []string{"/"}
		}},
		{"unclean path", func(c map[string]any) {
			release(c)["access"].(map[string]any)["apply"].(map[string]any)["writePaths"] = []string{"/boot/../etc"}
		}},
		{"missing schema", func(c map[string]any) { delete(release(c)["schemas"].(map[string]any), "checkpoint") }},
		{"schema digest mismatch", func(c map[string]any) {
			release(c)["schemas"].(map[string]any)["config"].(map[string]any)["digest"] = digest("other")
		}},
		{"external schema", func(c map[string]any) {
			s := release(c)["schemas"].(map[string]any)["config"].(map[string]any)
			raw := `{"$ref":"https://example.invalid/schema"}`
			s["bytes"] = raw
			s["digest"] = digest(raw)
		}},
		{"invalid schema", func(c map[string]any) {
			s := release(c)["schemas"].(map[string]any)["config"].(map[string]any)
			raw := `{"type":42}`
			s["bytes"] = raw
			s["digest"] = digest(raw)
		}},
		{"permissive publisher", func(c map[string]any) {
			repolicy(t, release(c), func(p map[string]any) { p["type"] = "insecureAcceptAnything" })
		}},
		{"key path", func(c map[string]any) { repolicy(t, release(c), func(p map[string]any) { p["keyPath"] = "/etc/key" }) }},
		{"invalid publisher key", func(c map[string]any) { repolicy(t, release(c), func(p map[string]any) { p["keyData"] = "bad" }) }},
		{"non exact identity", func(c map[string]any) {
			repolicy(t, release(c), func(p map[string]any) { p["signedIdentity"].(map[string]any)["type"] = "matchRepoDigestOrExact" })
		}},
		{"publisher digest mismatch", func(c map[string]any) { release(c)["publisher"].(map[string]any)["policyDigest"] = digest("wrong") }},
		{"oversized schema", func(c map[string]any) {
			s := release(c)["schemas"].(map[string]any)["config"].(map[string]any)
			raw := strings.Repeat(" ", 256*1024+1)
			s["bytes"] = raw
			s["digest"] = digest(raw)
		}},
		{"too many releases", func(c map[string]any) {
			rs := make([]any, 257)
			for i := range rs {
				rs[i] = release(c)
			}
			c["releases"] = rs
		}},
		{"conflicting active revocation", func(c map[string]any) {
			c["revocations"] = []any{map[string]any{"name": "orin", "version": "1.0.0", "descriptorDigest": digest("descriptor"), "reason": "revoked"}}
		}},
	}
	for _, tc := range cases {
		t.Run("When "+tc.name+" it should reject the whole candidate", func(t *testing.T) {
			c := fixture(t)
			tc.mutate(c)
			_, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
			require.Error(t, err)
		})
	}
	t.Run("When valid it should preserve exact schemas and distinct evidence aliases", func(t *testing.T) {
		c, err := updatercatalog.ParseAdministratorConfig(encode(t, fixture(t)))
		require.NoError(t, err)
		r := c.Releases[0]
		require.NotEqual(t, r.Schemas.Config.Digest, r.Schemas.Observation.Digest)
		e, err := r.RetainedEvidence()
		require.NoError(t, err)
		require.Len(t, e, 2)
		require.Equal(t, r.Schemas.Config.Digest, e[r.Schemas.Config.Digest].SchemaDigest)
		require.Equal(t, r.Schemas.Observation.Bytes, string(e[r.Schemas.Config.Digest].ObservationSchema))
		require.Equal(t, e[r.Schemas.Config.Digest].BudgetKeySet, e[r.Schemas.Observation.Digest].BudgetKeySet)
	})
	for _, input := range []string{"revision: 1\nrevision: 2", "apiVersion: firmware-catalog/v1\nrevision: &r 1\nreleases: *r", "apiVersion: firmware-catalog/v1\nrevision: 1\n---\nrevision: 2", `{"revision":1,"revision":2}`, strings.Repeat(" ", 4*1024*1024+1)} {
		t.Run("When ambiguous input it should reject", func(t *testing.T) {
			_, e := updatercatalog.ParseAdministratorConfig([]byte(input))
			require.Error(t, e)
		})
	}
}

func TestCatalogIdentity(t *testing.T) {
	a := fixture(t)
	original := encode(t, a)
	c, err := updatercatalog.ParseAdministratorConfig(original)
	require.NoError(t, err)
	first, err := c.Digest()
	require.NoError(t, err)
	pretty, err := json.MarshalIndent(a, "", "  ")
	require.NoError(t, err)
	again, err := updatercatalog.ParseAdministratorConfig(pretty)
	require.NoError(t, err)
	second, err := again.Digest()
	require.NoError(t, err)
	require.Equal(t, first, second)
	before, err := c.Releases[0].Digest()
	require.NoError(t, err)
	c.Releases[0].Limits.MaxCycles++
	after, err := c.Releases[0].Digest()
	require.NoError(t, err)
	require.NotEqual(t, before, after)
}

func TestRetainedEvidenceConflicts(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("When shared lookup identity has conflicting evidence %t it should validate consistently", conflict), func(t *testing.T) {
			c := fixture(t)
			r := release(c)
			clone := map[string]any{}
			require.NoError(t, json.Unmarshal(encode(t, r), &clone))
			clone["version"] = "2.0.0"
			if conflict {
				clone["limits"].(map[string]any)["maxCycles"] = 2048
				schema := clone["schemas"].(map[string]any)["observation"].(map[string]any)
				raw := `{"type":"object","additionalProperties":false,"properties":{"different":{"type":"boolean"}}}`
				schema["bytes"] = raw
				schema["digest"] = digest(raw)
			}
			c["releases"] = append(c["releases"].([]any), clone)
			_, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
			if conflict {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCatalogYAMLAndSchemaContracts(t *testing.T) {
	t.Run("When YAML input is valid it should retain exact schema bytes", func(t *testing.T) {
		c := fixture(t)
		data, err := yaml.Marshal(c)
		require.NoError(t, err)
		parsed, err := updatercatalog.ParseAdministratorConfig(data)
		require.NoError(t, err)
		fromJSON, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
		require.NoError(t, err)
		require.Equal(t, fromJSON, parsed)
	})
	for _, raw := range []string{`true`, `{"type":"object"}`, `{"type":"object","additionalProperties":false,"properties":{"item":{"type":["null","object"]}}}`, `{"type":"object","additionalProperties":false,"properties":{},"$defs":{"x":{"$ref":"file:///etc/passwd"}}}`, `{"type":"object","type":"string","additionalProperties":false}`, `{"type":"object","additionalProperties":false,"properties":{"n":` + strings.Repeat(`{"allOf":[`, 65) + `true` + strings.Repeat(`]}`, 65) + `}}}`, `{"type":"object","additionalProperties":false,"properties":{"open":{"properties":{"known":{"type":"string"}}}}}`} {
		t.Run("When schema is not closed or bounded it should reject", func(t *testing.T) {
			c := fixture(t)
			s := release(c)["schemas"].(map[string]any)["config"].(map[string]any)
			s["bytes"] = raw
			s["digest"] = digest(raw)
			_, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
			require.Error(t, err)
		})
	}
	t.Run("When schema bytes change only in whitespace it should require a new digest", func(t *testing.T) {
		c := fixture(t)
		s := release(c)["schemas"].(map[string]any)["config"].(map[string]any)
		s["bytes"] = s["bytes"].(string) + " "
		_, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
		require.Error(t, err)
	})
	t.Run("When a local schema ref is used it should compile without network", func(t *testing.T) {
		c := fixture(t)
		s := release(c)["schemas"].(map[string]any)["config"].(map[string]any)
		raw := `{"type":"object","additionalProperties":false,"properties":{"x":{"$ref":"#/$defs/value"}},"$defs":{"value":{"type":"string"}}}`
		s["bytes"] = raw
		s["digest"] = digest(raw)
		_, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
		require.NoError(t, err)
	})
}

func TestBudgetEvidenceIdentity(t *testing.T) {
	t.Run("When shared schema keys have different budget evidence it should reject", func(t *testing.T) {
		c := fixture(t)
		r := release(c)
		clone := map[string]any{}
		require.NoError(t, json.Unmarshal(encode(t, r), &clone))
		clone["version"] = "2.0.0"
		clone["budgetKeySet"] = []string{"maxReboots"}
		c["releases"] = append(c["releases"].([]any), clone)
		_, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
		require.Error(t, err)
	})
	t.Run("When shared keys have reordered budget sets it should accept normalized evidence", func(t *testing.T) {
		c := fixture(t)
		r := release(c)
		clone := map[string]any{}
		require.NoError(t, json.Unmarshal(encode(t, r), &clone))
		clone["version"] = "2.0.0"
		clone["budgetKeySet"] = []string{"maxAttempts", "maxCycles"}
		c["releases"] = append(c["releases"].([]any), clone)
		_, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
		require.NoError(t, err)
	})
	t.Run("When config and observation digests are equal it should collapse aliases", func(t *testing.T) {
		c := fixture(t)
		s := release(c)["schemas"].(map[string]any)
		s["observation"] = s["config"]
		p, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
		require.NoError(t, err)
		e, err := p.Releases[0].RetainedEvidence()
		require.NoError(t, err)
		require.Len(t, e, 1)
	})
	for _, keys := range [][]string{nil, {"unknown"}, {"maxCycles", "maxCycles"}} {
		t.Run("When budget key evidence is invalid it should reject", func(t *testing.T) {
			c := fixture(t)
			release(c)["budgetKeySet"] = keys
			_, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
			require.Error(t, err)
		})
	}
	t.Run("When an alias is mutated it should leave other evidence independent", func(t *testing.T) {
		c, err := updatercatalog.ParseAdministratorConfig(encode(t, fixture(t)))
		require.NoError(t, err)
		r := c.Releases[0]
		e, err := r.RetainedEvidence()
		require.NoError(t, err)
		e[r.Schemas.Config.Digest].BudgetKeySet[0] = "changed"
		require.NotEqual(t, e[r.Schemas.Config.Digest].BudgetKeySet, e[r.Schemas.Observation.Digest].BudgetKeySet)
		require.NotEqual(t, e[r.Schemas.Config.Digest].BudgetKeySet, r.BudgetKeySet)
	})
}

func TestAdditionalCatalogBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"excessive timeout", func(c map[string]any) { release(c)["limits"].(map[string]any)["maxInvocationSeconds"] = 86401 }},
		{"unknown dependency", func(c map[string]any) {
			release(c)["dependencies"] = []any{map[string]any{"name": "", "constraint": "1"}}
		}},
		{"duplicate dependency", func(c map[string]any) {
			d := release(c)["dependencies"].([]any)
			release(c)["dependencies"] = append(d, d[0])
		}},
		{"bad reference path", func(c map[string]any) {
			release(c)["referencePaths"] = []any{map[string]any{"path": "", "kind": "artifact"}}
		}},
		{"bad reference kind", func(c map[string]any) {
			release(c)["referencePaths"] = []any{map[string]any{"path": "source.repository", "kind": "command"}}
		}},
		{"bad GUID path", func(c map[string]any) { release(c)["guidPaths"] = []string{"resources..guid"} }},
		{"verify workspace write", func(c map[string]any) {
			release(c)["access"].(map[string]any)["verify"].(map[string]any)["workspaceWrite"] = true
		}},
		{"missing phase access", func(c map[string]any) { delete(release(c)["access"].(map[string]any), "verify") }},
		{"credential without network grant", func(c map[string]any) {
			release(c)["access"].(map[string]any)["prepare"].(map[string]any)["credentialRepositories"] = []string{"other"}
		}},
		{"duplicate network repository", func(c map[string]any) {
			release(c)["access"].(map[string]any)["prepare"].(map[string]any)["networkRepositories"] = []string{"artifacts", "artifacts"}
		}},
		{"nondevice path", func(c map[string]any) {
			release(c)["access"].(map[string]any)["apply"].(map[string]any)["writeDevices"] = []string{"/etc/config"}
		}},
		{"tagged publisher repository", func(c map[string]any) {
			repolicy(t, release(c), func(p map[string]any) {
				p["signedIdentity"].(map[string]any)["dockerRepository"] = "quay.io/example/updater:latest"
			})
		}},
		{"unqualified publisher repository", func(c map[string]any) {
			repolicy(t, release(c), func(p map[string]any) { p["signedIdentity"].(map[string]any)["dockerRepository"] = "updater" })
		}},
		{"invalid PEM key", func(c map[string]any) {
			repolicy(t, release(c), func(p map[string]any) { p["keyData"] = base64.StdEncoding.EncodeToString([]byte("not pem")) })
		}},
		{"invalid PKIX key", func(c map[string]any) {
			repolicy(t, release(c), func(p map[string]any) {
				p["keyData"] = base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("invalid")}))
			})
		}},
	}
	for _, tc := range cases {
		t.Run("When "+tc.name+" it should reject", func(t *testing.T) {
			c := fixture(t)
			tc.mutate(c)
			_, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
			require.Error(t, err)
		})
	}
	t.Run("When metadata paths and device access are explicit it should accept", func(t *testing.T) {
		c := fixture(t)
		r := release(c)
		r["referencePaths"] = []any{map[string]any{"path": "releases[].source.repository", "kind": "artifact"}}
		r["guidPaths"] = []string{"resources[].guid"}
		r["templatablePaths"] = []string{"board"}
		r["access"].(map[string]any)["apply"].(map[string]any)["writeDevices"] = []string{"/dev/example"}
		_, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
		require.NoError(t, err)
	})
	t.Run("When a candidate contains only tombstones it should accept", func(t *testing.T) {
		c := fixture(t)
		c["releases"] = []any{}
		c["revocations"] = []any{map[string]any{"name": "orin", "version": "1.0.0", "descriptorDigest": digest("descriptor"), "reason": "revoked"}}
		_, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
		require.NoError(t, err)
	})
	t.Run("When JSON is malformed it should reject", func(t *testing.T) {
		_, err := updatercatalog.ParseAdministratorConfig([]byte(`{"apiVersion":`))
		require.Error(t, err)
	})
}

func TestStrictIntegerFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"fractional revision", func(c map[string]any) { c["revision"] = 1.9 }},
		{"fractional protocol", func(c map[string]any) { release(c)["protocol"] = 1.9 }},
		{"fractional limit", func(c map[string]any) { release(c)["limits"].(map[string]any)["maxCycles"] = 1.9 }},
		{"fractional publisher version", func(c map[string]any) { repolicy(t, release(c), func(p map[string]any) { p["version"] = 1.9 }) }},
	}
	for _, tc := range cases {
		t.Run("When "+tc.name+" it should reject without truncation", func(t *testing.T) {
			c := fixture(t)
			tc.mutate(c)
			_, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
			require.Error(t, err)
		})
	}
}

func TestLiteralSchemaDataAndReferenceRoundTrip(t *testing.T) {
	t.Run("When literal schema data contains schema-like fields it should accept", func(t *testing.T) {
		c := fixture(t)
		s := release(c)["schemas"].(map[string]any)["config"].(map[string]any)
		raw := `{"type":"object","additionalProperties":false,"properties":{"payload":{"const":{"$ref":"literal-data","type":"object"}}},"examples":[{"type":"object"}]}`
		s["bytes"] = raw
		s["digest"] = digest(raw)
		_, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
		require.NoError(t, err)
	})
	t.Run("When reference paths are serialized it should parse them again", func(t *testing.T) {
		c := fixture(t)
		release(c)["referencePaths"] = []any{map[string]any{"path": "source.repository", "kind": "artifact"}}
		parsed, err := updatercatalog.ParseAdministratorConfig(encode(t, c))
		require.NoError(t, err)
		data, err := json.Marshal(parsed)
		require.NoError(t, err)
		again, err := updatercatalog.ParseAdministratorConfig(data)
		require.NoError(t, err)
		require.Equal(t, parsed, again)
	})
}
