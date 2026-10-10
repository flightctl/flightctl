package auxiliary

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestResolveAgentDeviceImage(t *testing.T) {
	projectRoot := t.TempDir()
	t.Chdir(projectRoot)
	if err := os.WriteFile("go.mod", []byte("module bundle-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundleDir := filepath.Join(projectRoot, "bin", "agent-artifacts")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bundle, err := os.Create(filepath.Join(bundleDir, "agent-images-bundle-test.tar"))
	if err != nil {
		t.Fatal(err)
	}
	image := "quay.io/custom/device:base-test-version"
	manifest, err := json.Marshal([]manifestEntry{{RepoTags: []string{image}}})
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(bundle)
	if err := writer.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(manifest))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveAgentDeviceImage("test"); err != nil || got != image {
		t.Fatalf("ResolveAgentDeviceImage() = (%q, %v), want (%q, nil)", got, err, image)
	}
}

func TestCopyImageFromBundleCanceledContext(t *testing.T) {
	t.Run("When the parent context is canceled it should not attempt an image copy", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var services Services
		if err := services.copyImageFromBundle(ctx, "bundle.tar", "quay.io/test/image:latest"); !errors.Is(err, context.Canceled) {
			t.Fatalf("copyImageFromBundle() error = %v, want context.Canceled", err)
		}
	})
}

func TestCopyImageFromBundleCanceledDuringCopy(t *testing.T) {
	t.Run("When the parent is canceled during skopeo it should return the context error", func(t *testing.T) {
		binDir := t.TempDir()
		marker := filepath.Join(binDir, "copy-started")
		skopeoPath := filepath.Join(binDir, "skopeo")
		if err := os.WriteFile(skopeoPath, []byte("#!/bin/sh\ntouch \"$COPY_STARTED\"\nexec sleep 30\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(skopeoPath, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("COPY_STARTED", marker)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		services := Services{Registry: &Registry{URL: "localhost:5000"}}
		result := make(chan error, 1)
		go func() {
			result <- services.copyImageFromBundle(ctx, "bundle.tar", "quay.io/test/image:latest")
		}()
		for {
			if _, err := os.Stat(marker); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("skopeo did not start before the test deadline")
			case <-time.After(10 * time.Millisecond):
			}
		}
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("copyImageFromBundle() error = %v, want context.Canceled", err)
		}
	})
}

// writeOCIBundleTar mirrors create_bundle() in
// test/scripts/agent-images/scripts/build_and_qcow2.sh, which produces the agent image bundle CI
// stages for every e2e shard:
//
//	tar -C "${staging}" -cf "${bundle_tar}" oci e2e-refs.tsv
//
// Note there is no manifest.json: the bundle is an OCI layout plus a "<tag>\t<ref>" index.
func writeOCIBundleTar(t *testing.T, path string, refs [][2]string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	writer := tar.NewWriter(file)
	addEntry := func(name, content string) {
		t.Helper()
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	addEntry(ociLayoutMarker, `{"imageLayoutVersion":"1.0.0"}`)
	addEntry("oci/index.json", `{"schemaVersion":2}`)
	var index strings.Builder
	for _, ref := range refs {
		index.WriteString(ref[0] + "\t" + ref[1] + "\n")
	}
	addEntry(e2eRefsFileName, index.String())
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExtractImageRefsOCIBundle(t *testing.T) {
	t.Run("When the bundle is an OCI layout it should read the refs from the e2e-refs index", func(t *testing.T) {
		bundle := filepath.Join(t.TempDir(), "agent-images-bundle-test.tar")
		writeOCIBundleTar(t, bundle, [][2]string{
			{"v2", "quay.io/flightctl/flightctl-device:v2"},
			{"base-test-version", "quay.io/flightctl/flightctl-device:base-test-version"},
		})

		got, err := extractImageRefs(bundle)
		if err != nil {
			t.Fatalf("extractImageRefs() error = %v, want nil", err)
		}
		want := []string{
			"quay.io/flightctl/flightctl-device:v2",
			"quay.io/flightctl/flightctl-device:base-test-version",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("extractImageRefs() = %v, want %v", got, want)
		}
	})

	t.Run("When the bundle has neither index it should report both expected entries", func(t *testing.T) {
		bundle := filepath.Join(t.TempDir(), "agent-images-bundle-test.tar")
		file, err := os.Create(bundle)
		if err != nil {
			t.Fatal(err)
		}
		writer := tar.NewWriter(file)
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}

		_, err = extractImageRefs(bundle)
		if err == nil {
			t.Fatal("extractImageRefs() error = nil, want an error")
		}
		for _, want := range []string{"manifest.json", e2eRefsFileName} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("extractImageRefs() error = %q, want it to mention %q", err, want)
			}
		}
	})
}

// TestResolveAgentDeviceImageOCIBundle is the regression test for the container-backed e2e device
// failing in CI: every shard resolves its device image from the staged agent bundle, which is an
// OCI-layout bundle, while resolution only understood docker-archive manifest.json bundles.
func TestResolveAgentDeviceImageOCIBundle(t *testing.T) {
	t.Run("When the staged bundle is an OCI layout it should resolve the base-tagged image", func(t *testing.T) {
		projectRoot := t.TempDir()
		t.Chdir(projectRoot)
		if err := os.WriteFile("go.mod", []byte("module bundle-test\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		bundleDir := filepath.Join(projectRoot, "bin", "agent-artifacts")
		if err := os.MkdirAll(bundleDir, 0o755); err != nil {
			t.Fatal(err)
		}
		image := "quay.io/flightctl/flightctl-device:base-cs9-bootc-v1.4.0-main-192"
		writeOCIBundleTar(t, filepath.Join(bundleDir, "agent-images-bundle-cs9-bootc.tar"), [][2]string{
			{"package", "quay.io/flightctl/flightctl-device:package"},
			{"base-cs9-bootc-v1.4.0-main-192", image},
		})

		if got, err := ResolveAgentDeviceImage("cs9-bootc"); err != nil || got != image {
			t.Fatalf("ResolveAgentDeviceImage() = (%q, %v), want (%q, nil)", got, err, image)
		}
	})
}
