package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/internal/imagebuilder_worker/vm"
)

// stageGuestAssets copies only task and service-image material, never worker credentials.
func (c *Consumer) stageGuestAssets(ctx context.Context, session *vm.Session, images []string) (bool, error) {
	for _, path := range []string{containerAuthFile, destinationAuthFile} {
		if err := writeGuestFile(ctx, session, path, []byte(`{"auths":{}}`)); err != nil {
			return false, err
		}
	}
	registries := map[string]bool{}
	imagePaths := []string{}
	for _, image := range images {
		ref, err := reference.ParseNormalizedNamed(image)
		if err != nil {
			return false, fmt.Errorf("invalid service image: %w", err)
		}
		registries[reference.Domain(ref)] = true
		imagePaths = append(imagePaths, ref.Name())
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	authPath := filepath.Join(home, ".config/containers/auth.json")
	if configured := os.Getenv("REGISTRY_AUTH_FILE"); configured != "" {
		authPath = configured
	}
	authData, err := os.ReadFile(authPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read service image credentials: %w", err)
	}
	var auth struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if len(authData) > 0 {
		if err := json.Unmarshal(authData, &auth); err != nil {
			return false, fmt.Errorf("parse service image credentials: %w", err)
		}
	}
	scoped := struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}{Auths: map[string]json.RawMessage{}}
	for key, value := range auth.Auths {
		prefix := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(key, "https://"), "http://"), "/")
		if prefix == "index.docker.io/v1" || prefix == "index.docker.io" {
			prefix = "docker.io"
		}
		for _, imagePath := range imagePaths {
			if imagePath == prefix || strings.HasPrefix(imagePath, prefix+"/") {
				scoped.Auths[key] = value
				break
			}
		}
	}

	data, err := json.Marshal(scoped)
	if err != nil {
		return false, err
	}
	if err := writeGuestFile(ctx, session, serviceAuthFile, data); err != nil {
		return false, err
	}
	for registry := range registries {
		if _, err := copyGuestDirectory(ctx, session, filepath.Join("/etc/containers/certs.d", registry)); err != nil {
			return false, err
		}
	}
	entitlement := false
	for _, path := range []string{entitlementCertsPath, "/etc/yum.repos.d", "/etc/rhsm"} {
		copied, err := copyGuestDirectory(ctx, session, path)
		if err != nil {
			return false, err
		}
		if path == entitlementCertsPath {
			entitlement = copied
		}
	}
	return entitlement, nil
}

func copyGuestDirectory(ctx context.Context, session *vm.Session, path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	copied := false
	for _, entry := range entries {
		// Kubernetes Secret projection uses hidden symlink entries beside visible keys.
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		file := filepath.Join(path, entry.Name())
		info, err := os.Stat(file)
		if err != nil {
			return false, err
		}
		if info.IsDir() {
			nested, err := copyGuestDirectory(ctx, session, file)
			if err != nil {
				return false, err
			}
			copied = copied || nested
			continue
		}
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("guest asset must be a regular file: %s", file)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return false, err
		}
		if err := writeGuestFile(ctx, session, file, data); err != nil {
			return false, err
		}
		copied = true
	}
	return copied, nil
}
