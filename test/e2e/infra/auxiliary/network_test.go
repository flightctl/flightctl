package auxiliary

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAlreadyConnectedMessage pins the split connectToNetwork depends on: an
// existing attachment is reuse and must succeed, while every other failure has
// to stop startup rather than leave an unreachable internal address published.
func TestAlreadyConnectedMessage(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{
			name:   "When podman reports the container is already attached it should be treated as reuse",
			stderr: "Error: container 5edb7fcd5a9b is already connected to network e2e-aux: network is already connected",
			want:   true,
		},
		{
			name:   "When docker reports the endpoint already exists it should be treated as reuse",
			stderr: "Error response from daemon: endpoint with name e2e-model-registry already exists in network e2e-aux",
			want:   true,
		},
		{
			name:   "When the network does not exist it should not be treated as reuse",
			stderr: `Error: unable to find network with name or ID e2e-aux: network not found`,
			want:   false,
		},
		{
			name:   "When the container does not exist it should not be treated as reuse",
			stderr: "Error: no container with name or ID \"e2e-model-registry\" found: no such container",
			want:   false,
		},
		{
			name:   "When the runtime socket is unreachable it should not be treated as reuse",
			stderr: "Error: unable to connect to Podman socket: dial unix /run/podman/podman.sock: connect: no such file or directory",
			want:   false,
		},
		{
			name:   "When the runtime reports nothing it should not be treated as reuse",
			stderr: "",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, alreadyConnectedMessage(tt.stderr))
		})
	}
}
