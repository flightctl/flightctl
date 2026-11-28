package registryconfig

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/pkg/userutil"
)

const (
	authName = "auth.json"
	authMode = 0o600
)

type output struct {
	account userutil.Account
	data    []byte
}

type desiredOutputs struct {
	files    map[string]output
	authored map[string]bool
}

type authEntry struct {
	Auth string `json:"auth"`
}

type accountAuth struct {
	account userutil.Account
	entries map[string]authEntry
}

func (m *manager) plan(ctx context.Context, desired *v1beta1.DeviceSpec) (desiredOutputs, error) {
	if err := ctx.Err(); err != nil {
		return desiredOutputs{}, err
	}
	registries, err := validateResources(desired.ConfigResources)
	if err != nil {
		return desiredOutputs{}, err
	}
	groups, err := m.groupAccess(ctx, registries)
	if err != nil || len(groups) == 0 {
		return desiredOutputs{}, err
	}
	authored, err := authoredPaths(desired.Config)
	if err != nil {
		return desiredOutputs{}, err
	}
	plan := desiredOutputs{files: make(map[string]output), authored: authored}
	rw, err := m.factory(v1beta1.CurrentProcessUsername)
	if err != nil {
		return desiredOutputs{}, err
	}
	for _, path := range slices.Sorted(maps.Keys(groups)) {
		group := groups[path]
		a, err := openDestination(rw, group.account, false, true)
		if err != nil {
			return desiredOutputs{}, fmt.Errorf("%w: resolve destination: %w", deviceerrors.ErrInvalidConfigResource, err)
		}
		if a != nil {
			_ = a.Close() // The read-only destination check has finished.
		}
		if len(group.entries) == 0 || authored[path] {
			continue
		}
		data, err := json.Marshal(struct {
			Auths map[string]authEntry `json:"auths"`
		}{group.entries})
		if err != nil {
			return desiredOutputs{}, fmt.Errorf("encode container authentication: %w", err)
		}
		plan.files[path] = output{account: group.account, data: data}
	}
	return plan, nil
}

func validateResources(resources *[]v1beta1.ConfigResource) ([]v1beta1.RegistryConfigResource, error) {
	if resources == nil {
		return nil, nil
	}
	if err := errors.Join(v1beta1.ValidateConfigResources(*resources)...); err != nil {
		return nil, fmt.Errorf("%w: %w", deviceerrors.ErrInvalidConfigResource, err)
	}
	registries := make([]v1beta1.RegistryConfigResource, 0, len(*resources))
	for _, resource := range *resources {
		reg, err := resource.AsRegistryConfigResource()
		if err != nil {
			return nil, fmt.Errorf("%w: decode registry resource", deviceerrors.ErrInvalidConfigResource)
		}
		for _, access := range reg.Access {
			for _, consumer := range access.Consumers {
				if consumer.Type != v1beta1.Container {
					return nil, fmt.Errorf("%w: unsupported registry consumer %q", deviceerrors.ErrInvalidConfigResource, consumer.Type)
				}
			}
		}
		registries = append(registries, reg)
	}
	return registries, nil
}

func authoredPaths(specs *[]v1beta1.ConfigProviderSpec) (map[string]bool, error) {
	files, err := config.ProviderSpecToFiles(specs)
	if err != nil {
		return nil, fmt.Errorf("%w: decode authored config paths", deviceerrors.ErrInvalidConfigResource)
	}
	paths := make(map[string]bool, len(files))
	for _, file := range files {
		paths[file.Path] = true
	}
	return paths, nil
}

func (m *manager) groupAccess(ctx context.Context, registries []v1beta1.RegistryConfigResource) (map[string]*accountAuth, error) {
	groups := make(map[string]*accountAuth)
	accounts := make(map[v1beta1.Username]userutil.Account)
	for _, reg := range registries {
		for _, access := range reg.Access {
			key := reg.Registry
			if *access.Scope != "" {
				key += "/" + *access.Scope
			}
			var entry authEntry
			if access.Authentication != nil {
				credential, err := access.Authentication.AsDockerAuth()
				if err != nil {
					return nil, fmt.Errorf("%w: decode registry authentication", deviceerrors.ErrInvalidConfigResource)
				}
				entry.Auth = base64.StdEncoding.EncodeToString([]byte(credential.Username + ":" + credential.Password))
			}
			for _, consumer := range access.Consumers {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				var user v1beta1.Username
				if consumer.User != nil {
					user = *consumer.User
				}
				account, ok := accounts[user]
				if !ok {
					var err error
					account, err = m.lookup(user)
					if err != nil {
						return nil, fmt.Errorf("%w: resolve execution account %q", deviceerrors.ErrInvalidConfigResource, user)
					}
					accounts[user] = account
				}
				if err := m.addAccess(groups, account, key, entry); err != nil {
					return nil, err
				}
			}
		}
	}
	return groups, nil
}

func (m *manager) addAccess(groups map[string]*accountAuth, account userutil.Account, key string, entry authEntry) error {
	path := filepath.Join(account.HomeDir, consts.ContainersAuthRelativePath)
	if !m.allowedHome(account.HomeDir) {
		return fmt.Errorf("%w: invalid or protected home %w", deviceerrors.ErrInvalidConfigResource, deviceerrors.WithElement(account.HomeDir))
	}
	group := groups[path]
	if group == nil {
		group = &accountAuth{account: account, entries: make(map[string]authEntry)}
		groups[path] = group
	}
	if group.account != account {
		return fmt.Errorf("%w: conflicting owners for %w", deviceerrors.ErrInvalidConfigResource, deviceerrors.WithElement(path))
	}
	if entry.Auth != "" {
		if prior, ok := group.entries[key]; ok && prior != entry {
			return fmt.Errorf("%w: conflicting authentication for registry scope %q at %w", deviceerrors.ErrInvalidConfigResource, key, deviceerrors.WithElement(path))
		}
		group.entries[key] = entry
	}
	return nil
}

func (m *manager) allowedHome(home string) bool {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home || home == "/" || strings.ContainsRune(home, 0) {
		return false
	}
	protected := filepath.Clean(m.dataDir)
	return !within(protected, home) && !within(protected, filepath.Join(home, consts.ContainersAuthRelativePath))
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// openDirectory keeps each lookup relative to an opened parent, including the
// components leading to a named user's home.
func openDirectory(rw fileio.ReadWriter, path string, readOnly bool) (fileio.Anchor, error) {
	opts := fileio.DirOptions{ReadOnly: readOnly}
	a, err := rw.OpenAnchor("/", opts)
	if err != nil {
		return nil, err
	}
	if path == "/" {
		return a, nil
	}
	for part := range strings.SplitSeq(strings.TrimPrefix(path, "/"), "/") {
		next, err := a.OpenDir(part, opts)
		_ = a.Close() // The child has its own descriptor.
		if err != nil {
			return nil, err
		}
		a = next
	}
	return a, nil
}

// openDestination returns a nil anchor when only the auth subdirectories are absent.
func openDestination(rw fileio.ReadWriter, account userutil.Account, create, readOnly bool) (fileio.Anchor, error) {
	a, err := openDirectory(rw, account.HomeDir, readOnly)
	if err != nil {
		return nil, err
	}
	info, err := a.Lstat(".")
	if err != nil || info.UID != account.UID {
		_ = a.Close() // Preserve the home validation error.
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("unexpected owner of %w", deviceerrors.WithElement(account.HomeDir))
	}
	owner := fileio.Ownership{UID: account.UID, GID: account.GID}
	for _, component := range []string{".config", "containers"} {
		if create {
			if err := a.Revalidate(); err != nil {
				_ = a.Close() // Preserve the path validation error.
				return nil, err
			}
		}
		next, err := a.OpenDir(component, fileio.DirOptions{Create: create, Owner: &owner})
		_ = a.Close() // OpenDir handles durability before returning.
		if !create && errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		a = next
	}
	info, err = a.Lstat(authName)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && info.IsRegular() && info.Nlink == 1) {
		return a, nil
	}
	_ = a.Close() // Preserve the auth file validation error.
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: %w", deviceerrors.ErrUnsafeFileType, deviceerrors.WithElement(filepath.Join(account.HomeDir, consts.ContainersAuthRelativePath)))
}
