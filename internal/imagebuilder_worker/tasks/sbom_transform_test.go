package tasks

import (
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/stretchr/testify/require"
)

func rpmRules(m map[string]string, d map[string]string, allowed []string) map[string]config.PurlTransformTypeRules {
	return map[string]config.PurlTransformTypeRules{
		"rpm": {NamespaceMapping: m, DistroMapping: d, AllowedQualifiers: allowed},
	}
}

func TestTransformPurl(t *testing.T) {
	t.Parallel()
	defaults := config.NewDefaultPurlTransformConfig()

	tests := []struct {
		name     string
		cfg      *config.PurlTransformConfig
		input    string
		expected string
	}{
		{
			name:     "When cfg is nil it should return the input unchanged",
			cfg:      nil,
			input:    "pkg:rpm/centos/acl@1.0?distro=centos-9",
			expected: "pkg:rpm/centos/acl@1.0?distro=centos-9",
		},
		{
			name: "When purl transform is disabled it should return the input unchanged",
			cfg: func() *config.PurlTransformConfig {
				disabled := false
				return &config.PurlTransformConfig{
					Enabled: &disabled,
					ByType:  rpmRules(map[string]string{"centos": "redhat"}, nil, []string{"arch", "distro"}),
				}
			}(),
			input:    "pkg:rpm/centos/acl@1.0?distro=centos-9&arch=x86_64",
			expected: "pkg:rpm/centos/acl@1.0?distro=centos-9&arch=x86_64",
		},
		{
			name:     "When the string is not a standard RPM PURL it should return unchanged",
			cfg:      defaults,
			input:    "not-a-purl",
			expected: "not-a-purl",
		},
		{
			name:     "When namespace maps it should replace namespace",
			cfg:      defaults,
			input:    "pkg:rpm/centos/acl@2.3-1.el9?arch=x86_64&distro=centos-9",
			expected: "pkg:rpm/redhat/acl@2.3-1.el9?arch=x86_64&distro=rhel-9",
		},
		{
			name:     "When distro is not in mapping it should keep distro value",
			cfg:      defaults,
			input:    "pkg:rpm/centos/acl@1.0?arch=x86_64&distro=kuku",
			expected: "pkg:rpm/redhat/acl@1.0?arch=x86_64&distro=kuku",
		},
		{
			name: "When distro mapping defines kuku it should replace distro",
			cfg: func() *config.PurlTransformConfig {
				enabled := true
				return &config.PurlTransformConfig{
					Enabled: &enabled,
					ByType: map[string]config.PurlTransformTypeRules{
						"rpm": {
							NamespaceMapping:  map[string]string{"centos": "redhat"},
							DistroMapping:     map[string]string{"kuku": "mapped-distro"},
							AllowedQualifiers: []string{"arch", "distro"},
						},
					},
				}
			}(),
			input:    "pkg:rpm/centos/foo@1.0?arch=amd64&distro=kuku",
			expected: "pkg:rpm/redhat/foo@1.0?arch=amd64&distro=mapped-distro",
		},
		{
			name:     "When qualifiers are not allowed it should strip them",
			cfg:      defaults,
			input:    "pkg:rpm/rocky/bash@5?arch=aarch64&distro=rocky-9&upstream=pkg:rpm/foo/bar&package-id=123",
			expected: "pkg:rpm/redhat/bash@5?arch=aarch64&distro=rhel-9",
		},
		{
			name:     "When namespace has mixed case it should still map using lowercase key",
			cfg:      defaults,
			input:    "pkg:rpm/CeNtOs/acl@1.0?distro=centos-9",
			expected: "pkg:rpm/redhat/acl@1.0?distro=rhel-9",
		},
		{
			name:     "When package type has no byType rule it should leave PURL unchanged including qualifiers",
			cfg:      defaults,
			input:    "pkg:npm/angular/core@17.0.0?vcs_url=srchost&checksum=deadbeef",
			expected: "pkg:npm/angular/core@17.0.0?vcs_url=srchost&checksum=deadbeef",
		},
		{
			name: "When byType defines npm with allowed qualifiers it should normalize npm PURLs only with those rules",
			cfg: func() *config.PurlTransformConfig {
				enabled := true
				return &config.PurlTransformConfig{
					Enabled: &enabled,
					ByType: map[string]config.PurlTransformTypeRules{
						"npm": {
							NamespaceMapping:  map[string]string{"angular": "@angular"},
							AllowedQualifiers: []string{"vcs_url"},
						},
					},
				}
			}(),
			input:    "pkg:npm/angular/core@17.0.0?vcs_url=repo&checksum=beef",
			expected: "pkg:npm/@angular/core@17.0.0?vcs_url=repo",
		},
		{
			name: "When byType rpm has empty allowed qualifiers it should preserve all qualifiers",
			cfg: func() *config.PurlTransformConfig {
				enabled := true
				return &config.PurlTransformConfig{
					Enabled: &enabled,
					ByType: map[string]config.PurlTransformTypeRules{
						"rpm": {
							NamespaceMapping:  map[string]string{"centos": "redhat"},
							AllowedQualifiers: []string{},
						},
					},
				}
			}(),
			input:    "pkg:rpm/centos/acl@1?arch=x86_64&distro=centos-9&extra=y",
			expected: "pkg:rpm/redhat/acl@1?arch=x86_64&distro=centos-9&extra=y",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := TransformPurl(tt.input, tt.cfg)
			require.Equal(t, tt.expected, got)
		})
	}
}

func TestTransformSBOMPurls(t *testing.T) {
	t.Parallel()
	defaults := config.NewDefaultPurlTransformConfig()

	t.Run("When transform is disabled it should return original bytes", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"components":[{"purl":"pkg:rpm/centos/a@1?distro=centos-9"}]}`)
		disabled := false
		cfg := &config.PurlTransformConfig{Enabled: &disabled}
		out, err := TransformSBOMPurls(raw, cfg)
		require.NoError(t, err)
		require.Equal(t, raw, out)
	})

	t.Run("When cfg is nil it should return original bytes", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"components":[{"purl":"pkg:rpm/centos/a@1"}]}`)
		out, err := TransformSBOMPurls(raw, nil)
		require.NoError(t, err)
		require.Equal(t, raw, out)
	})

	t.Run("When JSON is invalid it should return an error", func(t *testing.T) {
		t.Parallel()
		_, err := TransformSBOMPurls([]byte(`{`), defaults)
		require.Error(t, err)
	})

	t.Run("When components mix rpm and other types only types with rules transform", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{
  "components": [
    {"purl":"pkg:rpm/centos/acl@1.0?arch=x86_64&distro=centos-9&extra=q"},
    {"purl":"pkg:npm/foo/bar@1?keep=this&also=that"}
  ]
}`)
		out, err := TransformSBOMPurls(raw, defaults)
		require.NoError(t, err)
		var doc map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &doc))
		comps := doc["components"].([]interface{})
		require.Equal(t, "pkg:rpm/redhat/acl@1.0?arch=x86_64&distro=rhel-9", comps[0].(map[string]interface{})["purl"])
		require.Equal(t, "pkg:npm/foo/bar@1?keep=this&also=that", comps[1].(map[string]interface{})["purl"])
	})

	t.Run("When components have purls it should transform each", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{
  "components": [
    {"type":"library","name":"a","purl":"pkg:rpm/centos/acl@1.0?arch=x86_64&distro=centos-9"},
    {"type":"library","name":"b","purl":"pkg:rpm/rocky/bash@5?distro=rocky-9"}
  ]
}`)
		out, err := TransformSBOMPurls(raw, defaults)
		require.NoError(t, err)
		var doc map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &doc))
		comps := doc["components"].([]interface{})
		require.Len(t, comps, 2)
		c0 := comps[0].(map[string]interface{})
		c1 := comps[1].(map[string]interface{})
		require.Equal(t, "pkg:rpm/redhat/acl@1.0?arch=x86_64&distro=rhel-9", c0["purl"])
		require.Equal(t, "pkg:rpm/redhat/bash@5?distro=rhel-9", c1["purl"])
	})

	t.Run("When a component has no purl it should leave it unchanged", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"components":[{"type":"library","name":"x"}]}`)
		out, err := TransformSBOMPurls(raw, defaults)
		require.NoError(t, err)
		var doc map[string]interface{}
		require.NoError(t, json.Unmarshal(out, &doc))
		comps := doc["components"].([]interface{})
		c0 := comps[0].(map[string]interface{})
		require.Equal(t, "x", c0["name"])
		_, hasPurl := c0["purl"]
		require.False(t, hasPurl)
	})
}

func TestGetEffectivePurlTransformConfig(t *testing.T) {
	t.Parallel()

	t.Run("When userCfg is nil it should return defaults", func(t *testing.T) {
		t.Parallel()
		got := GetEffectivePurlTransformConfig(nil)
		def := config.NewDefaultPurlTransformConfig()
		require.Equal(t, def.ByType["rpm"].NamespaceMapping, got.ByType["rpm"].NamespaceMapping)
		require.Equal(t, def.ByType["rpm"].DistroMapping, got.ByType["rpm"].DistroMapping)
		require.Equal(t, def.ByType["rpm"].AllowedQualifiers, got.ByType["rpm"].AllowedQualifiers)
		require.True(t, got.EffectivePurlTransformEnabled())
	})

	t.Run("When user supplies only distro mapping for rpm it should merge namespace from defaults", func(t *testing.T) {
		t.Parallel()
		enabled := true
		user := &config.PurlTransformConfig{
			Enabled: &enabled,
			ByType: map[string]config.PurlTransformTypeRules{
				"rpm": {DistroMapping: map[string]string{"kuku": "other"}},
			},
		}
		got := GetEffectivePurlTransformConfig(user)
		def := config.NewDefaultPurlTransformConfig()
		require.Equal(t, "other", got.ByType["rpm"].DistroMapping["kuku"])
		require.Equal(t, "rhel-9", got.ByType["rpm"].DistroMapping["centos-9"])
		require.Equal(t, def.ByType["rpm"].NamespaceMapping["centos"], got.ByType["rpm"].NamespaceMapping["centos"])
	})

	t.Run("When user supplies only namespace mapping it should merge distro from defaults", func(t *testing.T) {
		t.Parallel()
		enabled := true
		user := &config.PurlTransformConfig{
			Enabled: &enabled,
			ByType: map[string]config.PurlTransformTypeRules{
				"rpm": {NamespaceMapping: map[string]string{"fedora": "redhat"}},
			},
		}
		got := GetEffectivePurlTransformConfig(user)
		def := config.NewDefaultPurlTransformConfig()
		require.Equal(t, "redhat", got.ByType["rpm"].NamespaceMapping["fedora"])
		require.Equal(t, def.ByType["rpm"].NamespaceMapping["centos"], got.ByType["rpm"].NamespaceMapping["centos"])
		require.Equal(t, "rhel-9", got.ByType["rpm"].DistroMapping["centos-9"])
	})

	t.Run("When user map keys differ only by casing they should normalize to lowercase overrides", func(t *testing.T) {
		t.Parallel()
		enabled := true
		user := &config.PurlTransformConfig{
			Enabled: &enabled,
			ByType: map[string]config.PurlTransformTypeRules{
				"RPM": {
					NamespaceMapping: map[string]string{"CentOS": "custom-ns"},
					DistroMapping:    map[string]string{"CentOS-9": "custom-distro"},
				},
			},
		}
		got := GetEffectivePurlTransformConfig(user)
		require.Equal(t, "custom-ns", got.ByType["rpm"].NamespaceMapping["centos"])
		require.Equal(t, "custom-distro", got.ByType["rpm"].DistroMapping["centos-9"])
		require.Equal(t, "redhat", got.ByType["rpm"].NamespaceMapping["rocky"])
	})

	t.Run("When user adds npm byType defaults rpm remains and npm merges", func(t *testing.T) {
		t.Parallel()
		enabled := true
		user := &config.PurlTransformConfig{
			Enabled: &enabled,
			ByType: map[string]config.PurlTransformTypeRules{
				"npm": {
					NamespaceMapping:  map[string]string{"angular": "@angular"},
					AllowedQualifiers: []string{"vcs_url"},
				},
			},
		}
		got := GetEffectivePurlTransformConfig(user)
		require.Contains(t, got.ByType, "rpm")
		require.Equal(t, "@angular", got.ByType["npm"].NamespaceMapping["angular"])
		p := "pkg:npm/angular/core@1?vcs_url=x&checksum=y"
		require.Equal(t, "pkg:npm/@angular/core@1?vcs_url=x", TransformPurl(p, got))
	})
}
