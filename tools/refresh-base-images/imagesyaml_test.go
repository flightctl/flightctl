package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const testImagesYAML = `api:
  image: quay.io/flightctl/flightctl-api-el10
  tag: latest
  build_base: registry.access.redhat.com/ubi10/go-toolset:1.26.7-1787775323
  run_base: quay.io/flightctl/flightctl-base:10.1-1769518576
cli-artifacts:
  image: quay.io/flightctl/flightctl-cli-artifacts-el10
  tag: latest
  build_base: registry.access.redhat.com/ubi10/go-toolset:1.26.7-1787775323
  run_base: registry.access.redhat.com/ubi10/ubi-minimal:10.1-1769677092
gateway:
  image: registry.access.redhat.com/ubi10/nginx-126
  tag: "1785834652"
`

func writeTestYAML(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "images.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// mustReadImagesYAML is a test helper that reads images.yaml and fails on error.
func mustReadImagesYAML(t *testing.T, path string) (ImagesYAML, *yaml.Node) {
	t.Helper()
	entries, doc, err := ReadImagesYAML(path)
	if err != nil {
		t.Fatalf("ReadImagesYAML(%s) error = %v", path, err)
	}
	return entries, doc
}

func TestSplitImageRef(t *testing.T) {
	tests := []struct {
		name      string
		ref       string
		wantImage string
		wantTag   string
	}{
		{
			name:      "When given a full image ref it should split on last colon",
			ref:       "registry.access.redhat.com/ubi10/go-toolset:1.26.7-1787775323",
			wantImage: "registry.access.redhat.com/ubi10/go-toolset",
			wantTag:   "1.26.7-1787775323",
		},
		{
			name:      "When given a ref with port it should split on last colon",
			ref:       "localhost:5000/myimage:v1",
			wantImage: "localhost:5000/myimage",
			wantTag:   "v1",
		},
		{
			name:      "When given a ref without tag it should return empty tag",
			ref:       "quay.io/flightctl/flightctl-base",
			wantImage: "quay.io/flightctl/flightctl-base",
			wantTag:   "",
		},
		{
			name:      "When given an empty string it should return empty",
			ref:       "",
			wantImage: "",
			wantTag:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img, tag := SplitImageRef(tt.ref)
			if img != tt.wantImage {
				t.Errorf("image = %q, want %q", img, tt.wantImage)
			}
			if tag != tt.wantTag {
				t.Errorf("tag = %q, want %q", tag, tt.wantTag)
			}
		})
	}
}

func TestReadImagesYAML(t *testing.T) {
	t.Run("When given a valid images.yaml it should parse entries with combined base refs", func(t *testing.T) {
		dir := t.TempDir()
		path := writeTestYAML(t, dir, testImagesYAML)

		entries, doc, err := ReadImagesYAML(path)
		if err != nil {
			t.Fatalf("ReadImagesYAML() error = %v", err)
		}
		if doc == nil {
			t.Fatal("expected non-nil document node")
		}

		api, ok := entries["api"]
		if !ok {
			t.Fatal("missing 'api' entry")
		}
		if api.BuildBase != "registry.access.redhat.com/ubi10/go-toolset:1.26.7-1787775323" {
			t.Errorf("api.BuildBase = %q", api.BuildBase)
		}
		if api.RunBase != "quay.io/flightctl/flightctl-base:10.1-1769518576" {
			t.Errorf("api.RunBase = %q", api.RunBase)
		}
	})

	t.Run("When entry has no base fields it should parse with empty strings", func(t *testing.T) {
		dir := t.TempDir()
		path := writeTestYAML(t, dir, testImagesYAML)

		entries, _, err := ReadImagesYAML(path)
		if err != nil {
			t.Fatalf("ReadImagesYAML() error = %v", err)
		}

		gw := entries["gateway"]
		if gw.BuildBase != "" {
			t.Errorf("gateway should have empty build_base, got %q", gw.BuildBase)
		}
	})
}

func TestGetCurrentTagForImage(t *testing.T) {
	t.Run("When image matches build_base prefix it should return the tag", func(t *testing.T) {
		dir := t.TempDir()
		path := writeTestYAML(t, dir, testImagesYAML)
		entries, _ := mustReadImagesYAML(t, path)

		tag, err := GetCurrentTagForImage(entries, "registry.access.redhat.com/ubi10/go-toolset")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tag != "1.26.7-1787775323" {
			t.Errorf("expected 1.26.7-1787775323, got %s", tag)
		}
	})

	t.Run("When image matches run_base prefix it should return the tag", func(t *testing.T) {
		dir := t.TempDir()
		path := writeTestYAML(t, dir, testImagesYAML)
		entries, _ := mustReadImagesYAML(t, path)

		tag, err := GetCurrentTagForImage(entries, "quay.io/flightctl/flightctl-base")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tag != "10.1-1769518576" {
			t.Errorf("expected 10.1-1769518576, got %s", tag)
		}
	})

	t.Run("When image not found it should return ErrBaseImageNotFound", func(t *testing.T) {
		dir := t.TempDir()
		path := writeTestYAML(t, dir, testImagesYAML)
		entries, _ := mustReadImagesYAML(t, path)

		_, err := GetCurrentTagForImage(entries, "nonexistent/image")
		if err == nil {
			t.Error("expected error for unknown image")
		}
		if !errors.Is(err, ErrBaseImageNotFound) {
			t.Errorf("expected ErrBaseImageNotFound, got: %v", err)
		}
	})
}

func TestContainersWithBase(t *testing.T) {
	t.Run("When searching for go-toolset it should return containers with build_base field", func(t *testing.T) {
		dir := t.TempDir()
		path := writeTestYAML(t, dir, testImagesYAML)
		entries, _ := mustReadImagesYAML(t, path)

		result := ContainersWithBase(entries, "registry.access.redhat.com/ubi10/go-toolset")
		if len(result) != 2 {
			t.Fatalf("expected 2 containers, got %d", len(result))
		}
		for _, r := range result {
			if r.Field != "build_base" {
				t.Errorf("expected build_base field, got %s", r.Field)
			}
		}
	})

	t.Run("When searching for flightctl-base it should return run_base field", func(t *testing.T) {
		dir := t.TempDir()
		path := writeTestYAML(t, dir, testImagesYAML)
		entries, _ := mustReadImagesYAML(t, path)

		result := ContainersWithBase(entries, "quay.io/flightctl/flightctl-base")
		if len(result) != 1 {
			t.Fatalf("expected 1 container, got %d", len(result))
		}
		if result[0].Name != "api" || result[0].Field != "run_base" {
			t.Errorf("unexpected result: %+v", result[0])
		}
	})
}

func TestUpdateNodeField(t *testing.T) {
	t.Run("When updating an existing field it should change the value", func(t *testing.T) {
		dir := t.TempDir()
		path := writeTestYAML(t, dir, testImagesYAML)
		_, doc := mustReadImagesYAML(t, path)

		err := UpdateNodeField(doc, "api", "run_base", "quay.io/flightctl/flightctl-base:10.2-9999999999")
		if err != nil {
			t.Fatalf("UpdateNodeField() error = %v", err)
		}

		if err := WriteImagesYAML(path, doc); err != nil {
			t.Fatal(err)
		}

		// Re-read and verify.
		entries, _ := mustReadImagesYAML(t, path)
		if entries["api"].RunBase != "quay.io/flightctl/flightctl-base:10.2-9999999999" {
			t.Errorf("expected updated ref, got %s", entries["api"].RunBase)
		}
	})

	t.Run("When container not found it should return error", func(t *testing.T) {
		dir := t.TempDir()
		path := writeTestYAML(t, dir, testImagesYAML)
		_, doc := mustReadImagesYAML(t, path)

		err := UpdateNodeField(doc, "nonexistent", "run_base", "foo:1.0")
		if err == nil {
			t.Error("expected error for unknown container")
		}
	})
}

func TestWriteImagesYAMLRoundTrip(t *testing.T) {
	t.Run("When writing after update it should preserve other fields", func(t *testing.T) {
		dir := t.TempDir()
		path := writeTestYAML(t, dir, testImagesYAML)
		_, doc := mustReadImagesYAML(t, path)

		// Update one field.
		_ = UpdateNodeField(doc, "api", "build_base", "registry.access.redhat.com/ubi10/go-toolset:1.27.0-9999999999")
		if err := WriteImagesYAML(path, doc); err != nil {
			t.Fatal(err)
		}

		// Verify other fields preserved.
		data, _ := os.ReadFile(path)
		content := string(data)
		if !strings.Contains(content, "quay.io/flightctl/flightctl-api-el10") {
			t.Error("original image field was lost")
		}
		if !strings.Contains(content, "1.27.0-9999999999") {
			t.Error("updated tag not found")
		}
		if !strings.Contains(content, "gateway") {
			t.Error("gateway entry was lost")
		}

		// Verify consistent 2-space indentation for nested fields.
		for _, line := range strings.Split(content, "\n") {
			if len(line) == 0 || line[0] != ' ' {
				continue // top-level key or blank line
			}
			if !strings.HasPrefix(line, "  ") {
				t.Errorf("expected 2-space indentation, got: %q", line)
			}
			// Nested fields must not be indented deeper than 2 spaces.
			trimmed := strings.TrimLeft(line, " ")
			indent := len(line) - len(trimmed)
			if indent != 2 {
				t.Errorf("expected exactly 2-space indent for field, got %d spaces: %q", indent, line)
			}
		}
	})
}

func TestImagesYAMLPath(t *testing.T) {
	t.Run("When given version 9 it should return el9 path", func(t *testing.T) {
		path := ImagesYAMLPath("packaging/images", "9")
		if path != "packaging/images/el9/images.yaml" {
			t.Errorf("expected packaging/images/el9/images.yaml, got %s", path)
		}
	})
}
