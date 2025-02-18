package hook

import (
	"os"
	"os/exec"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/stretchr/testify/require"
)

func TestCheckRunActionDependency(t *testing.T) {
	require := require.New(t)
	tempDir := t.TempDir()

	readWriter := fileio.NewReadWriter()
	readWriter.SetRootdir(tempDir)
	err := readWriter.WriteFile("executable.sh", []byte("#!/bin/bash\necho 'Hello'"), 0755)
	require.NoError(err)
	err = readWriter.WriteFile("non-executable.txt", []byte("Just some text"), 0644)
	require.NoError(err)
	err = readWriter.MkdirAll("subdir", 0755)
	require.NoError(err)

	ogPath := os.Getenv("PATH")
	newPath := tempDir
	require.NoError(os.Setenv("PATH", newPath))
	t.Cleanup(func() {
		_ = os.Setenv("PATH", ogPath)
	})

	tests := []struct {
		name    string
		action  v1alpha1.HookActionRun
		wantErr error
	}{
		{
			name:   "valid executable",
			action: v1alpha1.HookActionRun{Run: "executable.sh"},
		},
		{
			name:    "non-executable file",
			action:  v1alpha1.HookActionRun{Run: "non-executable.txt"},
			wantErr: exec.ErrNotFound,
		},
		{
			name:    "directory instead of file",
			action:  v1alpha1.HookActionRun{Run: "subdir"},
			wantErr: exec.ErrNotFound,
		},
		{
			name:    "invalid path",
			action:  v1alpha1.HookActionRun{Run: "/invalid/path/to/executable"},
			wantErr: errors.ErrNotExist,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkRunActionDependency(tt.action)
			if tt.wantErr != nil {
				require.ErrorIs(err, tt.wantErr)
				return
			} else {
				require.NoError(err)
			}
		})
	}
}
