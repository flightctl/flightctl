package imagebuilder_worker_test

import (
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/imagebuilder_worker/tasks"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// TestSBOMHelmLikeYAMLConfig unmarshals the YAML shape rendered under imageBuilderWorker.sbom in the worker config
// and verifies it merges with defaults and transforms CycloneDX PURLs as the worker would.
func TestSBOMHelmLikeYAMLConfig(t *testing.T) {
	t.Parallel()
	const fragment = `
enabled: true
pushToRegistry: true
uploadToTrustify: true
purlTransform:
  enabled: true
  distroMapping:
    kuku: custom-distro
`
	var sbom config.SBOMConfig
	require.NoError(t, yaml.Unmarshal([]byte(fragment), &sbom))
	require.True(t, sbom.Enabled)

	eff := tasks.GetEffectivePurlTransformConfig(sbom.PurlTransform)
	require.Equal(t, "custom-distro", eff.DistroMapping["kuku"])
	require.Equal(t, "redhat", eff.NamespaceMapping["centos"])

	raw := []byte(`{"components":[{"purl":"pkg:rpm/centos/tool@1.0?arch=x86_64&distro=kuku"}]}`)
	out, err := tasks.TransformSBOMPurls(raw, eff)
	require.NoError(t, err)
	var doc map[string]interface{}
	require.NoError(t, json.Unmarshal(out, &doc))
	comp := doc["components"].([]interface{})[0].(map[string]interface{})
	require.Equal(t, "pkg:rpm/redhat/tool@1.0?arch=x86_64&distro=custom-distro", comp["purl"])
}

// TestSBOMJSONPartialPurlTransform unmarshals JSON matching the worker config file shape for purlTransform only.
func TestSBOMJSONPartialPurlTransform(t *testing.T) {
	t.Parallel()
	const jsonFragment = `{
  "enabled": true,
  "namespaceMapping": {"fedora": "redhat"},
  "allowedQualifiers": ["distro"]
}`
	var pt config.PurlTransformConfig
	require.NoError(t, json.Unmarshal([]byte(jsonFragment), &pt))

	eff := tasks.GetEffectivePurlTransformConfig(&pt)
	require.Equal(t, "redhat", eff.NamespaceMapping["fedora"])
	require.Equal(t, "rhel-9", eff.DistroMapping["centos-9"])

	p := "pkg:rpm/fedora/pkg@1?distro=centos-9&arch=x86_64"
	got := tasks.TransformPurl(p, eff)
	// Only "distro" is in allowedQualifiers, so arch is dropped from the rebuilt PURL.
	require.Equal(t, "pkg:rpm/redhat/pkg@1?distro=rhel-9", got)
}
