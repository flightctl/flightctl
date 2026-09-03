package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckContainerfiles(t *testing.T) {
	cases := []struct {
		name      string
		el9       bool
		el10      bool
		wantIssue bool
	}{
		{
			name:      "When missing flavour it should report",
			el9:       true,
			el10:      false,
			wantIssue: true,
		},
		{
			name:      "When both flavours exist it should pass",
			el9:       true,
			el10:      true,
			wantIssue: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, osName := range []string{"el9", "el10"} {
				dir := filepath.Join(root, "packaging/images", osName)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.el9 {
				if err := os.WriteFile(filepath.Join(root, "packaging/images/el9/Containerfile.api"), []byte("FROM scratch\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.el10 {
				if err := os.WriteFile(filepath.Join(root, "packaging/images/el10/Containerfile.api"), []byte("FROM scratch\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			issues := checkContainerfiles(root, []ExpandedService{{Name: "api", BuildContainer: true}})
			if tc.wantIssue && len(issues) == 0 {
				t.Fatal("expected issue")
			}
			if !tc.wantIssue && len(issues) != 0 {
				t.Fatalf("unexpected issues: %v", issues)
			}
		})
	}
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
		{
			name:  "When only prefix host it should report",
			nginx: "proxy_pass http://flightctl-api-v2:3080;\n",
			services: []ExpandedService{
				{Name: "api", RequireGateway: true},
			},
			wantCount: 1,
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
	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "When matrix image list matches it should pass",
			content: "jobs:\n  build:\n    strategy:\n      matrix:\n        image: ['api', 'worker']\n",
		},
		{
			name:    "When SUPPORTED_IMAGES matches it should pass",
			content: "env:\n  SUPPORTED_IMAGES: \"api worker\"\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ".github/workflows")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "publish-containers.yaml"), []byte(tc.content), 0o644); err != nil {
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
}

func TestUnitWants(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
		absent  []string
	}{
		{
			name:    "When Wants is commented it should ignore",
			content: "[Unit]\n# Wants=flightctl-api.service\nWants=flightctl-worker.service\n[Install]\nWants=should-ignore.service\n",
			want:    []string{"flightctl-worker.service"},
			absent:  []string{"flightctl-api.service", "should-ignore.service"},
		},
		{
			name:    "When multiple units on one Wants it should split them",
			content: "[Unit]\nWants=flightctl-api.service flightctl-worker.service\n",
			want:    []string{"flightctl-api.service", "flightctl-worker.service"},
		},
		{
			name:    "When Wants continues with backslash it should include next line",
			content: "[Unit]\nWants=flightctl-api.service \\\n flightctl-worker.service\n",
			want:    []string{"flightctl-api.service", "flightctl-worker.service"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := unitWants(tc.content)
			for _, u := range tc.want {
				if _, ok := got[u]; !ok {
					t.Fatalf("missing %s in %v", u, got)
				}
			}
			for _, u := range tc.absent {
				if _, ok := got[u]; ok {
					t.Fatalf("unexpected %s", u)
				}
			}
		})
	}
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
		{
			name:    "When prefix host it should not match",
			content: "proxy_pass http://flightctl-api-v2:3080;\n",
			host:    "flightctl-api",
			want:    false,
		},
		{
			name:    "When host only in inline comment it should not match",
			content: "proxy_pass http://other:80; # flightctl-api\n",
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

func TestParseObservabilityOnlyImages(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
		skip []string
	}{
		{
			name: "When map entries are active it should include them",
			src:  "package p\nvar observabilityOnlyImages = map[string]bool{\n\t\"grafana\": true,\n\t\"prometheus\": true,\n}\n",
			want: []string{"grafana", "prometheus"},
		},
		{
			name: "When map entry is commented it should ignore it",
			src:  "package p\nvar observabilityOnlyImages = map[string]bool{\n\t\"grafana\": true,\n\t// \"prometheus\": true,\n}\n",
			want: []string{"grafana"},
			skip: []string{"prometheus"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseObservabilityOnlyImages(tc.src)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range tc.want {
				if _, ok := got[name]; !ok {
					t.Fatalf("missing %s in %v", name, got)
				}
			}
			for _, name := range tc.skip {
				if _, ok := got[name]; ok {
					t.Fatalf("commented %s should be ignored", name)
				}
			}
		})
	}
}

func TestCheckPodmanSave(t *testing.T) {
	t.Run("When required save is only commented it should report", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "deploy")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := "podman save flightctl-worker-$(OS):latest\n# podman save flightctl-api-$(OS):latest\n"
		if err := os.WriteFile(filepath.Join(dir, "deploy.mk"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		services := []ExpandedService{
			{Name: "api", BuildContainer: true, Publish: true},
			{Name: "worker", BuildContainer: true, Publish: true},
		}
		issues := checkPodmanSave(root, services)
		if len(issues) == 0 {
			t.Fatal("expected missing api save issue")
		}
	})
}

func TestParseCollectLogDeployments(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		want    []string
		skip    []string
		wantErr bool
	}{
		{
			name: "When loop is active it should include deployments",
			yaml: "runs:\n  steps:\n    - run: |\n        for deployment in flightctl-api flightctl-db; do\n          echo hi\n        done\n",
			want: []string{"api", "db"},
		},
		{
			name:    "When loop is only commented it should error",
			yaml:    "runs:\n  steps:\n    - run: |\n        # for deployment in flightctl-api flightctl-db; do\n        echo hi\n",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCollectLogDeployments(tc.yaml)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range tc.want {
				if _, ok := got[name]; !ok {
					t.Fatalf("missing %s in %v", name, got)
				}
			}
		})
	}
}

func TestParseRendererTagOverrides(t *testing.T) {
	src := `package renderer
type ImageConfig struct {
	Tag string
}
type RendererConfig struct {
	Api    ImageConfig ` + "`mapstructure:\"api\"`" + `
	Worker ImageConfig ` + "`mapstructure:\"worker\"`" + `
}
func (config *RendererConfig) ApplyFlightctlServicesTagOverride() {
	tag := "x"
	config.Api.Tag = tag
	// config.Worker.Tag = tag
}
`
	t.Run("When assignment is commented it should ignore it", func(t *testing.T) {
		got, err := parseRendererTagOverrides(src)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got["api"]; !ok {
			t.Fatalf("missing api in %v", got)
		}
		if _, ok := got["worker"]; ok {
			t.Fatal("commented worker assignment should be ignored")
		}
	})
}
