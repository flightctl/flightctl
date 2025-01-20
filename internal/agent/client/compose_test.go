package client

import (
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/stretchr/testify/require"
)

func TestParseComposeSpecFromDir(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name          string
		files         map[string][]byte
		expectedError error
		expectedSpec  ComposeSpec
	}{
		{
			name: "single compose.yaml file",
			files: map[string][]byte{
				"docker-compose.yaml": []byte(`
services:
  web:
    image: nginx
`),
			},
			expectedSpec: ComposeSpec{
				Services: map[string]ComposeService{
					"web": {Image: "nginx"},
				},
			},
		},
		{
			name: "single compose.yml file with yml override",
			files: map[string][]byte{
				"docker-compose.yml": []byte(`
services:
  web:
    image: nginx
`),
				"docker-compose.override.yml": []byte(`
services:
  web:
    image: nginx:latest
`),
			},
			expectedSpec: ComposeSpec{
				Services: map[string]ComposeService{
					"web": {Image: "nginx:latest"},
				},
			},
		},
		{
			name: "multiple compose files priority .yaml",
			files: map[string][]byte{
				"docker-compose.yaml": []byte(`
services:
  web:
    image: nginx
`),
				"docker-compose.yml": []byte(`
services:
  web:
    image: apache
`),
			},
			expectedSpec: ComposeSpec{
				Services: map[string]ComposeService{
					"web": {Image: "nginx"},
				},
			},
		},
		{
			name: "no compose files",
			files: map[string][]byte{
				"random-file.txt": []byte("not a compose file"),
			},
			expectedError: errors.ErrNoComposeFile,
			expectedSpec:  ComposeSpec{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			readerWriter := fileio.NewReadWriter()
			readerWriter.SetRootdir(tmpDir)
			for filename, content := range tt.files {
				if err := readerWriter.WriteFile(filename, content, fileio.DefaultFilePermissions); err != nil {
					require.NoError(err)
				}
			}
			spec, err := ParseComposeSpecFromDir(readerWriter, "/")
			if tt.expectedError != nil {
				require.ErrorIs(err, tt.expectedError)
				return
			}
			require.NoError(err)
			require.Equal(tt.expectedSpec, *spec)
		})
	}
}
