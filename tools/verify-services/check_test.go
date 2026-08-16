package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckContainerfiles(t *testing.T) {
	t.Run("When missing flavour it should report", func(t *testing.T) {
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

		services := []ExpandedService{{Name: "api", BuildContainer: true}}
		issues := checkContainerfiles(root, services)
		if len(issues) == 0 {
			t.Fatal("expected missing el10 containerfile issue")
		}
	})
}

func TestCheckNginx(t *testing.T) {
	cases := []struct {
		name      string
		nginx     string
		services  []ExpandedService
		wantCount int
	}{
		{
			name:  "When hostname missing it should report",
			nginx: "proxy_pass http://flightctl-api:3080;\n",
			services: []ExpandedService{
				{Name: "api", RequireGateway: true},
				{Name: "remote-access", RequireGateway: true},
			},
			wantCount: 1,
		},
		{
			name:  "When only commented it should report",
			nginx: "# proxy_pass http://flightctl-remote-access:3444/;\nproxy_pass http://flightctl-api:3080;\n",
			services: []ExpandedService{
				{Name: "remote-access", RequireGateway: true},
			},
			wantCount: 1,
		},
		{
			name:  "When proxy_pass present it should pass",
			nginx: "proxy_pass http://flightctl-remote-access:3444/;\n",
			services: []ExpandedService{
				{Name: "remote-access", RequireGateway: true},
			},
			wantCount: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "deploy/podman/flightctl-gateway/flightctl-gateway-config")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "nginx.conf.template"), []byte(tc.nginx), 0o644); err != nil {
				t.Fatal(err)
			}
			issues := checkNginx(root, tc.services)
			if len(issues) != tc.wantCount {
				t.Fatalf("issues=%v want %d", issues, tc.wantCount)
			}
		})
	}
}

func TestCheckPublishMatrix(t *testing.T) {
	t.Run("When matrix matches it should pass", func(t *testing.T) {
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
	})
}

func TestUnitWants(t *testing.T) {
	t.Run("When Wants is commented it should ignore", func(t *testing.T) {
		content := "[Unit]\n# Wants=flightctl-api.service\nWants=flightctl-worker.service\n[Install]\nWants=should-ignore.service\n"
		got := unitWants(content)
		if _, ok := got["flightctl-api.service"]; ok {
			t.Fatal("commented Wants should be ignored")
		}
		if _, ok := got["flightctl-worker.service"]; !ok {
			t.Fatal("active Wants missing")
		}
		if _, ok := got["should-ignore.service"]; ok {
			t.Fatal("Wants outside [Unit] should be ignored")
		}
	})
}

func TestHasNginxRoutingDirective(t *testing.T) {
	cases := []struct {
		name    string
		content string
		host    string
		want    bool
	}{
		{
			name:    "When proxy_pass present it should match",
			content: "proxy_pass http://flightctl-api:3080;\n",
			host:    "flightctl-api",
			want:    true,
		},
		{
			name:    "When upstream server present it should match",
			content: "server flightctl-api:3080;\n",
			host:    "flightctl-api",
			want:    true,
		},
		{
			name:    "When only commented it should not match",
			content: "# proxy_pass http://flightctl-api:3080;\n",
			host:    "flightctl-api",
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasNginxRoutingDirective(tc.content, tc.host); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
