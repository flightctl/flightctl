package packaging_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("could not find repo root")
		}
		root = parent
	}
}

func TestGreenbootConfigureServiceRemoved(t *testing.T) {
	root := repoRoot(t)

	removedPaths := []string{
		"packaging/greenboot/flightctl-configure-greenboot.sh",
		"packaging/systemd/flightctl-configure-greenboot.service",
	}
	for _, rel := range removedPaths {
		if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err=%v", rel, err)
		}
	}

	specPath := filepath.Join(root, "packaging/rpm/flightctl.spec")
	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	specText := string(spec)
	for _, forbidden := range []string{
		"packaging/greenboot/flightctl-configure-greenboot.sh",
		"packaging/systemd/flightctl-configure-greenboot.service",
		"systemctl enable flightctl-configure-greenboot.service",
	} {
		if strings.Contains(specText, forbidden) {
			t.Fatalf("flightctl.spec still references %q", forbidden)
		}
	}

	functionsPath := filepath.Join(root, "packaging/greenboot/functions.sh")
	functions, err := os.ReadFile(functionsPath)
	if err != nil {
		t.Fatalf("read functions.sh: %v", err)
	}
	for _, forbidden := range []string{
		"find_third_party_scripts",
		"find_blocked_vendor_healthchecks",
		"set_disabled_healthchecks",
		"DISABLED_HEALTHCHECKS",
		"DISABLED_VENDOR_HEALTHCHECKS",
	} {
		if strings.Contains(string(functions), forbidden) {
			t.Fatalf("functions.sh still contains %q", forbidden)
		}
	}
}
