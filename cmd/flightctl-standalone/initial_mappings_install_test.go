package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallInitialLabelSyncMappings(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../.."))
	helper := filepath.Join(repoRoot, "deploy/scripts/install_initial_label_sync_mappings.sh")
	defaultSource := filepath.Join(repoRoot, "packaging/flightctl/label-sync/mappings.yaml")

	t.Run("first install copies the packaged defaults", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "etc/flightctl/label-sync/mappings.yaml")
		runInitialMappingsInstaller(t, helper, defaultSource, destination)

		got, err := os.ReadFile(destination)
		if err != nil {
			t.Fatalf("read installed manifest: %v", err)
		}
		want, err := os.ReadFile(defaultSource)
		if err != nil {
			t.Fatalf("read packaged manifest: %v", err)
		}
		if string(got) != string(want) {
			t.Fatalf("installed manifest differs from packaged default")
		}
	})

	tests := []struct {
		name    string
		content string
	}{
		{name: "custom operator manifest", content: "operator-managed: true\n"},
		{name: "empty operator sequence", content: "[]\n"},
		{name: "zero-byte operator manifest", content: ""},
	}
	for _, tt := range tests {
		t.Run("preserves "+tt.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "etc/flightctl/label-sync/mappings.yaml")
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(destination, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}

			runInitialMappingsInstaller(t, helper, defaultSource, destination)
			got, err := os.ReadFile(destination)
			if err != nil {
				t.Fatalf("read preserved manifest: %v", err)
			}
			if string(got) != tt.content {
				t.Fatalf("manifest changed from %q to %q", tt.content, string(got))
			}
		})
	}
}

func runInitialMappingsInstaller(t *testing.T, helper, source, destination string) {
	t.Helper()
	cmd := exec.Command("bash", helper, source, destination)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("initial mappings installer failed: %v\n%s", err, output)
	}
}
