package flightctl_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkerEnrollmentSigningMounts(t *testing.T) {
	for _, mode := range []string{"builtin", "cert-manager", "none", "auto"} {
		for _, namespace := range []string{"public", "internal"} {
			t.Run("When certificates use "+mode+" it should mount existing signing material in "+namespace, func(t *testing.T) {
				output, err := catalogChart(t, "global.generateCertificates="+mode, "global.internalNamespace="+namespace)
				require.NoError(t, err, output)
				docs := catalogDocuments(t, output)
				deployment := docs["Deployment/flightctl-worker"]
				require.Equal(t, namespace, deployment["metadata"].(map[string]any)["namespace"])
				pod := deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
				container := namedEntry(pod["containers"], "flightctl-worker")
				require.Equal(t, "/root", namedEntry(container["env"], "HOME")["value"])
				mounts := container["volumeMounts"].([]any)
				for _, expected := range []struct{ path, volume, subpath string }{
					{"client-signer.crt", "flightctl-client-signer", "tls.crt"},
					{"client-signer.key", "flightctl-client-signer", "tls.key"},
					{"ca-bundle.crt", "flightctl-ca-bundle", "ca-bundle.crt"},
				} {
					found := false
					for _, value := range mounts {
						mount := value.(map[string]any)
						if mount["mountPath"] == "/root/.flightctl/certs/"+expected.path {
							require.Equal(t, expected.volume, mount["name"])
							require.Equal(t, expected.subpath, mount["subPath"])
							require.Equal(t, true, mount["readOnly"])
							found = true
						}
						require.NotEqual(t, "flightctl-api-server-tls", mount["name"])
					}
					require.True(t, found, expected.path)
				}
				for volume, secret := range map[string]string{"flightctl-client-signer": "flightctl-client-signer-ca", "flightctl-ca-bundle": "flightctl-ca-bundle"} {
					entry := namedEntry(pod["volumes"], volume)
					require.NotNil(t, entry)
					secretRef := entry["secret"].(map[string]any)
					require.Equal(t, secret, secretRef["secretName"])
					require.NotEqual(t, true, secretRef["optional"])
				}
			})
		}
	}
}
