package auxiliary

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
