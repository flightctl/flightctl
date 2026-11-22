package flightctl_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestWorkerEnrollmentSigningMounts(t *testing.T) {
	for _, mode := range []string{"builtin", "cert-manager", "none", "auto"} {
		for _, namespace := range []string{"public", "internal"} {
			t.Run("When certificates use "+mode+" it should allow missing enrollment material in "+namespace, func(t *testing.T) {
				output, err := catalogChart(t, "global.generateCertificates="+mode, "global.internalNamespace="+namespace)
				require.NoError(t, err, output)
				docs := catalogDocuments(t, output)
				deployment := docs["Deployment/flightctl-worker"]
				require.Equal(t, namespace, deployment["metadata"].(map[string]any)["namespace"])
				pod := deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
				container := namedEntry(pod["containers"], "flightctl-worker")
				mount := namedEntry(container["volumeMounts"], "enrollment-signing")
				require.NotNil(t, mount)
				require.Equal(t, "/root/.flightctl/certs/enrollment", mount["mountPath"])
				require.Equal(t, true, mount["readOnly"])
				require.NotContains(t, mount, "subPath")
				require.Nil(t, namedEntry(pod["volumes"], "flightctl-api-server-tls"))
				volume := namedEntry(pod["volumes"], "enrollment-signing")["projected"].(map[string]any)
				sources := volume["sources"].([]any)
				require.Len(t, sources, 2)
				for index, name := range []string{"flightctl-client-signer-ca", "flightctl-ca-bundle"} {
					secret := sources[index].(map[string]any)["secret"].(map[string]any)
					require.Equal(t, name, secret["name"])
					require.Equal(t, true, secret["optional"])
					require.NotEmpty(t, secret["items"])
					expected := []any{map[string]any{"key": "ca-bundle.crt", "path": "ca-bundle.crt"}}
					if index == 0 {
						expected = []any{map[string]any{"key": "tls.crt", "path": "client-signer.crt"}, map[string]any{"key": "tls.key", "path": "client-signer.key"}}
					}
					require.Equal(t, expected, secret["items"])
				}
				secret := namedEntry(pod["volumes"], "flightctl-encryption-key")["secret"].(map[string]any)
				require.NotEqual(t, true, secret["optional"])
				config := docs["ConfigMap/flightctl-worker-config"]["data"].(map[string]any)["config.yaml"].(string)
				require.Contains(t, config, "certStore: /root/.flightctl/certs/enrollment")
			})
		}
	}
}

func TestTPMTrustRendering(t *testing.T) {
	for _, configured := range []bool{false, true} {
		args := []string{"global.internalNamespace=internal"}
		if configured {
			args = append(args, "global.tpmCA.configMapName=manufacturer-roots", "global.tpmCA.keys={first.pem,second.crt}")
		}
		output, err := catalogChart(t, args...)
		require.NoError(t, err, output)
		docs := catalogDocuments(t, output)
		for _, service := range []string{"api", "worker"} {
			name := "flightctl-" + service
			pod := docs["Deployment/"+name]["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			mount := namedEntry(namedEntry(pod["containers"], name)["volumeMounts"], "tpm-ca")
			volume := namedEntry(pod["volumes"], "tpm-ca")
			var config struct {
				Service struct {
					TPMCAPaths []string `json:"tpmCAPaths"`
				} `json:"service"`
			}
			require.NoError(t, yaml.Unmarshal([]byte(docs["ConfigMap/"+name+"-config"]["data"].(map[string]any)["config.yaml"].(string)), &config))
			if !configured {
				require.Nil(t, mount)
				require.Nil(t, volume)
				require.Empty(t, config.Service.TPMCAPaths)
				continue
			}
			require.Equal(t, []string{"/etc/flightctl/tpm-cas/first.pem", "/etc/flightctl/tpm-cas/second.crt"}, config.Service.TPMCAPaths)
			require.Equal(t, "/etc/flightctl/tpm-cas", mount["mountPath"])
			require.Equal(t, true, mount["readOnly"])
			require.NotContains(t, mount, "subPath")
			ref := volume["configMap"].(map[string]any)
			require.Equal(t, "manufacturer-roots", ref["name"])
			require.Equal(t, true, ref["optional"])
			require.Len(t, ref["items"], 2)
		}
	}
	for _, keys := range []string{"{}", "{../root.pem}", "{/root.pem}", "{..}", "{same.pem,same.pem}"} {
		output, err := catalogChart(t, "global.tpmCA.configMapName=roots", "global.tpmCA.keys="+keys)
		require.Error(t, err, output)
	}
	output, err := catalogChart(t, "global.tpmCA.configMapName=roots")
	require.Error(t, err, output)
	output, err = catalogChart(t, "global.tpmCA.keys={root.pem}")
	require.Error(t, err, output)
}
