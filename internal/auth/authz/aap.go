package authz

import (
	"context"
	"os"
	"path/filepath"
	"slices"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/fsnotify/fsnotify"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

const (
	AAP_PERMISSIONS_CONFIG_FILE = "/etc/flightctl/permissions.yaml"
)

type PermissionsConfig map[string][]GroupPermissions

type GroupPermissions struct {
	Resources []string `json:"resources"`
	Ops       []string `json:"verbs"`
}

type AapAuthZ struct {
	permissionsConfig PermissionsConfig
}

func NewAapAuthZ(log logrus.FieldLogger) (*AapAuthZ, error) {
	pConfig, err := loadPermissionsConfig()
	if err != nil {
		return nil, err
	}
	aapAuthZ := &AapAuthZ{
		permissionsConfig: pConfig,
	}
	err = aapAuthZ.watchPermissionsConfig(log)
	return aapAuthZ, err
}

func loadPermissionsConfig() (PermissionsConfig, error) {
	content, err := os.ReadFile(AAP_PERMISSIONS_CONFIG_FILE)
	if err != nil {
		return nil, err
	}
	pConfig := PermissionsConfig{}
	if err := yaml.Unmarshal(content, &pConfig); err != nil {
		return nil, err
	}
	return pConfig, nil
}

// ConfigMap mounted as volume is a symlink
func getRealConfigPath() (string, error) {
	realConfigFile, err := filepath.EvalSymlinks(AAP_PERMISSIONS_CONFIG_FILE)
	return filepath.Dir(realConfigFile), err
}

func (a *AapAuthZ) watchPermissionsConfig(log logrus.FieldLogger) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	configFolder := filepath.Dir(AAP_PERMISSIONS_CONFIG_FILE)
	realConfigPath, err := getRealConfigPath()
	if err != nil {
		watcher.Close()
		return err
	}

	if err := watcher.Add(configFolder); err != nil {
		watcher.Close()
		return err
	}

	go func() {
		defer watcher.Close()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&fsnotify.Remove == fsnotify.Remove || event.Op&fsnotify.Write == fsnotify.Write {
					if event.Name == realConfigPath {
						pConfig, err := loadPermissionsConfig()
						if err != nil {
							log.WithError(err).Warn("Failed to load permissions config")
						}
						a.permissionsConfig = pConfig
						log.Info("group permissions updated")
						realConfigPath, err = getRealConfigPath()
						if err != nil {
							log.WithError(err).Warn("Failed to get new path of permissions config")
						}
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.WithError(err).Warn("Failed to watch permissions config")
			}

		}
	}()
	return nil
}

func (aapAuth AapAuthZ) CheckPermission(ctx context.Context, resource string, op string) (bool, error) {
	identity, err := common.GetIdentity(ctx)
	if err != nil {
		return false, err
	}

	for _, group := range identity.Groups {
		permissions, ok := aapAuth.permissionsConfig[group]
		if ok {
			for _, permission := range permissions {
				if isAllowed(permission.Resources, resource) && isAllowed(permission.Ops, op) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func isAllowed(defs []string, obj string) bool {
	return slices.Contains(defs, "*") || slices.Contains(defs, obj)
}
