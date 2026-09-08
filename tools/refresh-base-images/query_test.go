package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQueryField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "images.yaml")
	if err := os.WriteFile(path, []byte(testImagesYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		key     string
		field   string
		want    string
		wantErr bool
	}{
		{
			name:  "build_base field",
			key:   "api",
			field: "build_base",
			want:  "registry.access.redhat.com/ubi10/go-toolset:1.26.7-1787775323",
		},
		{
			name:  "run_base field",
			key:   "api",
			field: "run_base",
			want:  "quay.io/flightctl/flightctl-base:10.1-1769518576",
		},
		{
			name:  "run_base for cli-artifacts",
			key:   "cli-artifacts",
			field: "run_base",
			want:  "registry.access.redhat.com/ubi10/ubi-minimal:10.1-1769677092",
		},
		{
			name:  "image field via node tree",
			key:   "api",
			field: "image",
			want:  "quay.io/flightctl/flightctl-api-el10",
		},
		{
			name:  "tag field via node tree",
			key:   "gateway",
			field: "tag",
			want:  "1785834652",
		},
		{
			name:    "missing key",
			key:     "nonexistent",
			field:   "build_base",
			wantErr: true,
		},
		{
			name:    "missing field",
			key:     "api",
			field:   "nonexistent",
			wantErr: true,
		},
		{
			name:  "entry without build_base falls back to node tree",
			key:   "gateway",
			field: "image",
			want:  "registry.access.redhat.com/ubi10/nginx-126",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := queryField(path, tt.key, tt.field)
			if (err != nil) != tt.wantErr {
				t.Fatalf("queryField() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("queryField() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestQueryFieldFileNotFound(t *testing.T) {
	_, err := queryField("/nonexistent/images.yaml", "api", "build_base")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestRunQuery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "images.yaml")
	if err := os.WriteFile(path, []byte(testImagesYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{
			name:     "success",
			args:     []string{"--file", path, "--key", "api", "--field", "build_base"},
			wantCode: 0,
		},
		{
			name:     "missing key returns 1",
			args:     []string{"--file", path, "--key", "nonexistent", "--field", "build_base"},
			wantCode: 1,
		},
		{
			name:     "missing field returns 1",
			args:     []string{"--file", path, "--key", "api", "--field", "nonexistent"},
			wantCode: 1,
		},
		{
			name:     "missing flags returns 1",
			args:     []string{},
			wantCode: 1,
		},
		{
			name:     "nonexistent file returns 1",
			args:     []string{"--file", "/nonexistent/images.yaml", "--key", "api", "--field", "build_base"},
			wantCode: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := runQuery(tt.args)
			if code != tt.wantCode {
				t.Errorf("runQuery() = %d, want %d", code, tt.wantCode)
			}
		})
	}
}
