package firmwarepolicy_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/firmwarepolicy"
	catalog "github.com/flightctl/flightctl/internal/service/updatercatalog"
	"github.com/flightctl/flightctl/test/util/firmwarecatalog"
	"github.com/stretchr/testify/require"
)

func catalogRelease(t *testing.T) catalog.ReleaseBinding {
	t.Helper()
	cfg, err := firmwarecatalog.New()
	require.NoError(t, err)
	return cfg.Releases[0]
}

func bindDescriptor(t *testing.T, descriptor []byte) firmwarepolicy.Binding {
	t.Helper()
	var identity struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	require.NoError(t, json.Unmarshal(descriptor, &identity))
	return firmwarepolicy.Binding{Name: identity.Name, Version: identity.Version, DescriptorDigest: firmwarepolicy.Hash(descriptor), Descriptor: descriptor}
}

func canonical(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	var normalized any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	require.NoError(t, dec.Decode(&normalized))
	data, err = json.Marshal(normalized)
	require.NoError(t, err)
	return data
}

func mutatedBinding(t *testing.T, mutate func(m map[string]any)) firmwarepolicy.Binding {
	t.Helper()
	data, err := json.Marshal(catalogRelease(t))
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	mutate(m)
	data, err = json.Marshal(m)
	require.NoError(t, err)
	return bindDescriptor(t, data)
}

func object(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		m = m[k].(map[string]any)
	}
	return m
}

func rawBinding(t *testing.T, edit func(string) string) firmwarepolicy.Binding {
	t.Helper()
	data, err := json.Marshal(catalogRelease(t))
	require.NoError(t, err)
	edited := []byte(edit(string(data)))
	b := bindDescriptor(t, data)
	b.Descriptor = edited
	b.DescriptorDigest = firmwarepolicy.Hash(edited)
	return b
}

func TestDecodeDescriptor(t *testing.T) {
	release := catalogRelease(t)
	fixture, err := json.Marshal(release)
	require.NoError(t, err)

	t.Run("When the descriptor is a catalog release it should decode every identity", func(t *testing.T) {
		require := require.New(t)
		d, err := firmwarepolicy.DecodeDescriptor(bindDescriptor(t, fixture))
		require.NoError(err)
		require.Equal(release.Name, d.Name)
		require.Equal(release.Version, d.Version)
		require.Equal(release.PackageDigest, d.PackageDigest)
		require.Equal(release.Protocol, d.Protocol)
		require.Equal(release.Publisher.Identity, d.Publisher.Identity)
		require.Equal(release.Publisher.PolicyDigest, d.Publisher.PolicyDigest)
		require.Equal(release.Publisher.Policy.KeyData, d.Publisher.Policy.KeyData)
		require.Equal("quay.io/example/updater", d.Publisher.Policy.SignedIdentity.DockerRepository)
		require.Equal(release.Schemas.Config.Digest, d.Schemas.Config.Digest)
		require.Equal(release.Schemas.Checkpoint.Bytes, d.Schemas.Checkpoint.Bytes)
		require.NotNil(d.Access)
		require.True(d.Access.Prepare.WorkspaceWrite)
		require.Equal(release.Limits.MaxAttempts, d.Limits.MaxAttempts)
		require.Equal(release.BudgetKeySet, d.BudgetKeySet)
		require.Empty(d.Dependencies)
	})

	t.Run("When the descriptor uses the canonical catalog encoding it should decode", func(t *testing.T) {
		_, err := firmwarepolicy.DecodeDescriptor(bindDescriptor(t, canonical(t, release)))
		require.NoError(t, err)
	})

	t.Run("When the descriptor declares runtime dependencies it should decode them", func(t *testing.T) {
		require := require.New(t)
		b := mutatedBinding(t, func(m map[string]any) {
			m["dependencies"] = []any{map[string]any{"name": "fwupd", "constraint": ">=1.9"}}
		})
		d, err := firmwarepolicy.DecodeDescriptor(b)
		require.NoError(err)
		require.Equal([]firmwarepolicy.RuntimeDependency{{Name: "fwupd", Constraint: ">=1.9"}}, d.Dependencies)
	})

	schemaKeys := []string{"config", "observation", "request", "result", "checkpoint"}
	rejected := []struct {
		name    string
		binding func(t *testing.T) firmwarepolicy.Binding
	}{
		{"When the descriptor digest differs from the binding it should reject", func(t *testing.T) firmwarepolicy.Binding {
			b := bindDescriptor(t, fixture)
			b.DescriptorDigest = firmwarepolicy.Hash([]byte("other"))
			return b
		}},
		{"When the descriptor name differs from the binding it should reject", func(t *testing.T) firmwarepolicy.Binding {
			b := bindDescriptor(t, fixture)
			b.Name = "other"
			return b
		}},
		{"When the descriptor version differs from the binding it should reject", func(t *testing.T) firmwarepolicy.Binding {
			b := bindDescriptor(t, fixture)
			b.Version = "9.9.9"
			return b
		}},
		{"When the descriptor is empty it should reject", func(t *testing.T) firmwarepolicy.Binding {
			b := bindDescriptor(t, fixture)
			b.Descriptor = nil
			b.DescriptorDigest = firmwarepolicy.Hash(nil)
			return b
		}},
		{"When the descriptor has an unknown top-level field it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) { m["extra"] = 1 })
		}},
		{"When the publisher policy has an unknown field it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) { object(m, "publisher", "policy")["keyPath"] = "/etc/key.pub" })
		}},
		{"When an access grant has an unknown field it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) { object(m, "access", "prepare")["hostReboot"] = true })
		}},
		{"When a key differs from its field name only by case it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return rawBinding(t, func(s string) string { return strings.Replace(s, `"protocol":`, `"Protocol":`, 1) })
		}},
		{"When a nested key differs from its field name only by case it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return rawBinding(t, func(s string) string { return strings.Replace(s, `"keyData":`, `"KeyData":`, 1) })
		}},
		{"When a key inside a list element differs only by case it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) {
				m["dependencies"] = []any{map[string]any{"Name": "fwupd", "constraint": ">=1.9"}}
			})
		}},
		{"When a key is duplicated it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return rawBinding(t, func(s string) string { return strings.Replace(s, `{"name":`, `{"protocol":1,"name":`, 1) })
		}},
		{"When an object field has the wrong JSON type it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) { m["publisher"] = 5 })
		}},
		{"When a list field has the wrong JSON type it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) { m["budgetKeySet"] = "maxCycles" })
		}},
		{"When a numeric field is a string it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) { m["protocol"] = "1" })
		}},
		{"When the descriptor has trailing content it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return rawBinding(t, func(s string) string { return s + ` {}` })
		}},
		{"When the package digest is uppercase it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) { m["packageDigest"] = strings.ToUpper(m["packageDigest"].(string)) })
		}},
		{"When the package digest uses another algorithm it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) { m["packageDigest"] = "sha512:" + strings.Repeat("a", 64) })
		}},
		{"When the package digest is missing it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) { delete(m, "packageDigest") })
		}},
		{"When the publisher policy version is not 1 it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) { object(m, "publisher", "policy")["version"] = 2 })
		}},
		{"When the publisher policy is not sigstoreSigned it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) { object(m, "publisher", "policy")["type"] = "signedBy" })
		}},
		{"When the signed identity is not exactRepository it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) {
				object(m, "publisher", "policy", "signedIdentity")["type"] = "matchRepository"
			})
		}},
		{"When the publisher key data is empty it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) { object(m, "publisher", "policy")["keyData"] = "" })
		}},
		{"When the publisher key data is not base64 it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) { object(m, "publisher", "policy")["keyData"] = "not base64!" })
		}},
		{"When the publisher repository carries a tag it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) {
				object(m, "publisher", "policy", "signedIdentity")["dockerRepository"] = "quay.io/example/updater:latest"
			})
		}},
		{"When the publisher repository carries a digest it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) {
				object(m, "publisher", "policy", "signedIdentity")["dockerRepository"] = "quay.io/example/updater@sha256:" + strings.Repeat("a", 64)
			})
		}},
		{"When the publisher repository is not canonical it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) {
				object(m, "publisher", "policy", "signedIdentity")["dockerRepository"] = "example/updater"
			})
		}},
		{"When the publisher identity is empty it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) { object(m, "publisher")["identity"] = "" })
		}},
		{"When the publisher policy digest does not match the policy it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) {
				object(m, "publisher")["policyDigest"] = firmwarepolicy.Hash([]byte("other policy"))
			})
		}},
		{"When a schema digest is malformed it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) {
				object(m, "schemas", "config")["digest"] = strings.ToUpper(object(m, "schemas", "config")["digest"].(string))
			})
		}},
		{"When a schema document is empty it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) {
				object(m, "schemas", "request")["bytes"] = ""
				object(m, "schemas", "request")["digest"] = firmwarepolicy.Hash(nil)
			})
		}},
		{"When the access section is null it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) { m["access"] = nil })
		}},
		{"When the access section is missing it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) { delete(m, "access") })
		}},
		{"When requested access violates a catalog phase restriction it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) {
				object(m, "access", "verify")["writePaths"] = []any{"/opt/vendor"}
			})
		}},
		{"When requested access names a repository that is not a logical name it should reject", func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) {
				object(m, "access", "prepare")["networkRepositories"] = []any{"quay.io/vendor/fw"}
			})
		}},
	}
	for _, key := range schemaKeys {
		rejected = append(rejected, struct {
			name    string
			binding func(t *testing.T) firmwarepolicy.Binding
		}{fmt.Sprintf("When the %s schema digest differs from its exact bytes it should reject", key), func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) {
				object(m, "schemas", key)["bytes"] = `{"type":"object","additionalProperties":false}`
			})
		}})
	}
	for _, phase := range []string{"prepare", "apply", "verify"} {
		rejected = append(rejected, struct {
			name    string
			binding func(t *testing.T) firmwarepolicy.Binding
		}{fmt.Sprintf("When the %s access phase is null it should reject", phase), func(t *testing.T) firmwarepolicy.Binding {
			return mutatedBinding(t, func(m map[string]any) { object(m, "access")[phase] = nil })
		}})
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			d, err := firmwarepolicy.DecodeDescriptor(tc.binding(t))
			require.Error(err)
			require.Nil(d)
		})
	}
}

func TestPackageReference(t *testing.T) {
	require := require.New(t)
	release := catalogRelease(t)
	data, err := json.Marshal(release)
	require.NoError(err)
	d, err := firmwarepolicy.DecodeDescriptor(bindDescriptor(t, data))
	require.NoError(err)
	require.Equal("quay.io/example/updater@"+release.PackageDigest, d.PackageReference())
}

func TestDescriptorRoundTripsEveryCatalogField(t *testing.T) {
	release := catalogRelease(t)
	release.Dependencies = []catalog.RuntimeDependency{{Name: "fwupd", Constraint: ">=1.9 <2"}}
	release.ReferencePaths = []catalog.ReferencePath{{Path: "artifacts[].url", Kind: "artifact"}, {Path: "artifacts[].signature", Kind: "signature"}}
	release.GUIDPaths = []string{"targets[].guid"}
	release.TemplatablePaths = []string{"targets[].label"}
	release.Access = &catalog.PhaseAccess{
		Prepare: &catalog.AccessGrant{NetworkRepositories: []string{"vendor-firmware"}, CredentialRepositories: []string{"vendor-firmware"}, ReadPaths: []string{"/etc/vendor"}, ReadDevices: []string{"/dev/mtd0"}, WorkspaceWrite: true},
		Apply:   &catalog.AccessGrant{ReadPaths: []string{"/etc/vendor"}, WritePaths: []string{"/opt/vendor"}, ReadDevices: []string{"/dev/mtd0"}, WriteDevices: []string{"/dev/mtd0"}, WorkspaceWrite: true},
		Verify:  &catalog.AccessGrant{ReadPaths: []string{"/etc/vendor", "/opt/vendor"}, ReadDevices: []string{"/dev/mtd0"}},
	}
	cfg, err := firmwarecatalog.New()
	require.NoError(t, err)
	cfg.Releases = []catalog.ReleaseBinding{release}
	_, err = catalog.ParseAdministratorConfig(mustMarshal(t, cfg))
	require.NoError(t, err, "the fully populated release must be a valid catalog release")

	for _, encoding := range []struct {
		name string
		data []byte
	}{{"struct order", mustMarshal(t, release)}, {"canonical", canonical(t, release)}} {
		t.Run("When a fully populated release uses "+encoding.name+" encoding it should round-trip every field", func(t *testing.T) {
			require := require.New(t)
			d, err := firmwarepolicy.DecodeDescriptor(bindDescriptor(t, encoding.data))
			require.NoError(err)
			roundTrip, err := json.Marshal(d)
			require.NoError(err)
			require.JSONEq(string(encoding.data), string(roundTrip))
		})
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

type accessVector struct {
	name   string
	access *firmwarepolicy.PhaseAccess
	valid  bool
}

func grants(prepare, apply, verify firmwarepolicy.AccessGrant) *firmwarepolicy.PhaseAccess {
	return &firmwarepolicy.PhaseAccess{Prepare: &prepare, Apply: &apply, Verify: &verify}
}

func accessVectors() []accessVector {
	none := firmwarepolicy.AccessGrant{}
	name128 := "a" + strings.Repeat("b", 127)
	name129 := name128 + "c"
	vectors := []accessVector{
		{"When every phase is empty it should accept", grants(none, none, none), true},
		{"When access is nil it should reject", nil, false},
		{"When prepare is nil it should reject", &firmwarepolicy.PhaseAccess{Apply: &none, Verify: &none}, false},
		{"When apply is nil it should reject", &firmwarepolicy.PhaseAccess{Prepare: &none, Verify: &none}, false},
		{"When verify is nil it should reject", &firmwarepolicy.PhaseAccess{Prepare: &none, Apply: &none}, false},
		{"When prepare acquires network and matching credentials it should accept", grants(firmwarepolicy.AccessGrant{NetworkRepositories: []string{"vendor-firmware"}, CredentialRepositories: []string{"vendor-firmware"}}, none, none), true},
		{"When apply acquires network it should reject", grants(none, firmwarepolicy.AccessGrant{NetworkRepositories: []string{"vendor-firmware"}}, none), false},
		{"When verify acquires network it should reject", grants(none, none, firmwarepolicy.AccessGrant{NetworkRepositories: []string{"vendor-firmware"}}), false},
		{"When apply acquires credentials it should reject", grants(none, firmwarepolicy.AccessGrant{NetworkRepositories: []string{"vendor-firmware"}, CredentialRepositories: []string{"vendor-firmware"}}, none), false},
		{"When verify acquires credentials it should reject", grants(none, none, firmwarepolicy.AccessGrant{CredentialRepositories: []string{"vendor-firmware"}}), false},
		{"When apply writes host paths and devices it should accept", grants(none, firmwarepolicy.AccessGrant{WritePaths: []string{"/opt/vendor"}, WriteDevices: []string{"/dev/mtd0"}}, none), true},
		{"When prepare writes host paths it should reject", grants(firmwarepolicy.AccessGrant{WritePaths: []string{"/opt/vendor"}}, none, none), false},
		{"When prepare writes devices it should reject", grants(firmwarepolicy.AccessGrant{WriteDevices: []string{"/dev/mtd0"}}, none, none), false},
		{"When verify only reads it should accept", grants(none, none, firmwarepolicy.AccessGrant{ReadPaths: []string{"/opt/vendor"}, ReadDevices: []string{"/dev/mtd0"}}), true},
		{"When prepare and apply write their workspace it should accept", grants(firmwarepolicy.AccessGrant{WorkspaceWrite: true}, firmwarepolicy.AccessGrant{WorkspaceWrite: true}, none), true},
		{"When verify writes host paths it should reject", grants(none, none, firmwarepolicy.AccessGrant{WritePaths: []string{"/opt/vendor"}}), false},
		{"When verify writes devices it should reject", grants(none, none, firmwarepolicy.AccessGrant{WriteDevices: []string{"/dev/mtd0"}}), false},
		{"When verify writes its workspace it should reject", grants(none, none, firmwarepolicy.AccessGrant{WorkspaceWrite: true}), false},
		{"When a credential lacks a matching network grant it should reject", grants(firmwarepolicy.AccessGrant{NetworkRepositories: []string{"vendor-firmware"}, CredentialRepositories: []string{"other"}}, none, none), false},
		{"When a repository name has the maximum length it should accept", grants(firmwarepolicy.AccessGrant{NetworkRepositories: []string{name128}}, none, none), true},
		{"When a repository name is a dotted logical name it should accept", grants(firmwarepolicy.AccessGrant{NetworkRepositories: []string{"vendor.firmware_v2"}}, none, none), true},
		{"When a repository list repeats a name it should reject", grants(firmwarepolicy.AccessGrant{NetworkRepositories: []string{"vendor-firmware", "vendor-firmware"}}, none, none), false},
		{"When a path is clean and absolute it should accept", grants(firmwarepolicy.AccessGrant{ReadPaths: []string{"/opt/vendor"}}, none, none), true},
		{"When a path list repeats a path it should reject", grants(firmwarepolicy.AccessGrant{ReadPaths: []string{"/opt/vendor", "/opt/vendor"}}, none, none), false},
		{"When a device is under /dev it should accept", grants(firmwarepolicy.AccessGrant{ReadDevices: []string{"/dev/mtd0"}}, none, none), true},
	}
	for _, repo := range []string{"quay.io/vendor/fw", "https://quay.io/vendor", "vendor:1.0", "vendor@sha256:" + strings.Repeat("a", 64), "-vendor", ".vendor", name129, ""} {
		vectors = append(vectors, accessVector{fmt.Sprintf("When repository %q is not a logical name it should reject", repo), grants(firmwarepolicy.AccessGrant{NetworkRepositories: []string{repo}}, none, none), false})
	}
	for _, p := range []string{"opt/vendor", "/", "/opt/vendor/", "/opt//vendor", "/opt/./vendor", "/opt/vendor/..", "/opt/*", "/opt/vendor?", "/opt/[ab]", "/opt/a\x00b", "/opt/a\rb", "/opt/a\nb", ""} {
		vectors = append(vectors, accessVector{fmt.Sprintf("When path %q is not an explicit clean absolute path it should reject", p), grants(none, none, firmwarepolicy.AccessGrant{ReadPaths: []string{p}}), false})
	}
	for _, device := range []string{"/sys/class/mtd/mtd0", "/devx/mtd0", "/dev/../mtd0", "/dev"} {
		vectors = append(vectors, accessVector{fmt.Sprintf("When device %q is not a path under /dev it should reject", device), grants(none, firmwarepolicy.AccessGrant{WriteDevices: []string{device}}, none), false})
	}
	return vectors
}

func catalogAccepts(t *testing.T, access *firmwarepolicy.PhaseAccess) bool {
	t.Helper()
	cfg, err := firmwarecatalog.New()
	require.NoError(t, err)
	var converted *catalog.PhaseAccess
	require.NoError(t, json.Unmarshal(mustMarshal(t, access), &converted))
	cfg.Releases[0].Access = converted
	_, err = catalog.ParseAdministratorConfig(mustMarshal(t, cfg))
	return err == nil
}

func TestValidateAccess(t *testing.T) {
	for _, tc := range accessVectors() {
		t.Run(tc.name, func(t *testing.T) {
			err := firmwarepolicy.ValidateAccess(tc.access)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestValidateAccessMatchesCatalogRules(t *testing.T) {
	for _, tc := range accessVectors() {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			agentAccepts := firmwarepolicy.ValidateAccess(tc.access) == nil
			require.Equal(catalogAccepts(t, tc.access), agentAccepts, "agent and catalog access rules diverge")
			require.Equal(tc.valid, agentAccepts)
		})
	}
}
