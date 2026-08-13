package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckContainerfiles_WhenMissingFlavour_itShouldReport(t *testing.T) {
	root := t.TempDir()
	for _, osName := range []string{"el9", "el10"} {
		dir := filepath.Join(root, "packaging/images", osName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "packaging/images/el9/Containerfile.api"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// el10 missing

	services := []ExpandedService{{Name: "api", BuildContainer: true}}
	issues := checkContainerfiles(root, services)
	if len(issues) == 0 {
		t.Fatal("expected missing el10 containerfile issue")
	}
}

func TestCheckNginx_WhenHostnameMissing_itShouldReport(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "deploy/podman/flightctl-gateway/flightctl-gateway-config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nginx.conf.template"), []byte("proxy_pass http://flightctl-api:3080;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	services := []ExpandedService{
		{Name: "api", RequireNginx: true},
		{Name: "remote-access", RequireNginx: true},
	}
	issues := checkNginx(root, services)
	if len(issues) != 1 || issues[0].Check != "nginx" {
		t.Fatalf("issues=%v", issues)
	}
}

func TestCheckPublishMatrix_WhenMatches_itShouldPass(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".github/workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "jobs:\n  build:\n    strategy:\n      matrix:\n        image: ['api', 'worker']\n"
	if err := os.WriteFile(filepath.Join(dir, "publish-containers.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	services := []ExpandedService{
		{Name: "api", Publish: true},
		{Name: "worker", Publish: true},
	}
	if issues := checkPublishMatrix(root, services); len(issues) != 0 {
		t.Fatalf("unexpected issues: %v", issues)
	}
}
