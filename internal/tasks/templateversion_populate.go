package tasks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"

	config_latest "github.com/coreos/ignition/v2/config/v3_4"
	config_latest_types "github.com/coreos/ignition/v2/config/v3_4/types"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-git/go-billy/v5"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

func TemplateVersionPopulate(taskManager TaskManager) {
	for {
		select {
		case <-taskManager.ctx.Done():
			taskManager.log.Info("Received ctx.Done(), stopping")
			return
		case resourceRef := <-taskManager.channels[ChannelTemplateVersionPopulate]:
			requestID := reqid.NextRequestID()
			ctx := context.WithValue(context.Background(), middleware.RequestIDKey, requestID)
			log := log.WithReqIDFromCtx(ctx, taskManager.log)
			logic := NewTemplateVersionPopulateLogic(taskManager, log, taskManager.store, resourceRef)
			if resourceRef.Op == TemplateVersionPopulateOpCreated {
				err := logic.SyncFleetTemplateToTemplateVersion(ctx)
				if err != nil {
					log.Errorf("failed populating template version %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
				}
			} else {
				log.Errorf("TemplateVersionPopulate called with unexpected kind %s and op %s", resourceRef.Kind, resourceRef.Op)
			}
		}
	}
}

type TemplateVersionPopulateLogic struct {
	taskManager      TaskManager
	log              logrus.FieldLogger
	store            store.Store
	resourceRef      ResourceReference
	templateVersion  *api.TemplateVersion
	fleet            *api.Fleet
	unrenderedConfig []api.TemplateVersionStatus_Config_Item
	renderedConfig   *config_latest_types.Config
}

func NewTemplateVersionPopulateLogic(taskManager TaskManager, log logrus.FieldLogger, store store.Store, resourceRef ResourceReference) TemplateVersionPopulateLogic {
	return TemplateVersionPopulateLogic{taskManager: taskManager, log: log, store: store, resourceRef: resourceRef}
}

func (t *TemplateVersionPopulateLogic) SyncFleetTemplateToTemplateVersion(ctx context.Context) error {
	t.log.Infof("Syncing template of %s to template version %s/%s", t.resourceRef.Owner, t.resourceRef.OrgID, t.resourceRef.Name)
	err := t.getFleetAndTemplateVersion(ctx)
	if t.templateVersion == nil {
		if err != nil {
			return err
		}
		// non-fleet owner
		return nil
	}
	if err != nil {
		return t.setStatus(ctx, err)
	}

	if t.fleet.Spec.Template.Spec.Config != nil {
		t.unrenderedConfig = []api.TemplateVersionStatus_Config_Item{}
		emptyIgnitionConfig, _, _ := config_latest.ParseCompatibleVersion([]byte("{\"ignition\": {\"version\": \"3.4.0\"}"))
		t.renderedConfig = &emptyIgnitionConfig

		for i := range *t.fleet.Spec.Template.Spec.Config {
			configItem := (*t.fleet.Spec.Template.Spec.Config)[i]
			err := t.handleConfigItem(ctx, &configItem)
			if err != nil {
				return t.setStatus(ctx, err)
			}
		}
	}
	return t.setStatus(ctx, nil)
}

func (t *TemplateVersionPopulateLogic) getFleetAndTemplateVersion(ctx context.Context) error {
	ownerType, fleetName, err := util.GetResourceOwner(&t.resourceRef.Owner)
	if err != nil {
		return err
	}
	if ownerType != model.FleetKind {
		return nil
	}

	templateVersion, err := t.store.TemplateVersion().Get(ctx, t.resourceRef.OrgID, fleetName, t.resourceRef.Name)
	if err != nil {
		return fmt.Errorf("failed fetching templateVersion: %w", err)
	}
	t.templateVersion = templateVersion

	fleet, err := t.store.Fleet().Get(ctx, t.resourceRef.OrgID, fleetName)
	if err != nil {
		return fmt.Errorf("failed fetching fleet: %w", err)
	}
	t.fleet = fleet

	return nil
}

func (t *TemplateVersionPopulateLogic) handleConfigItem(ctx context.Context, configItem *api.DeviceSpec_Config_Item) error {
	disc, err := configItem.Discriminator()
	if err != nil {
		return fmt.Errorf("failed getting discriminator: %w", err)
	}

	switch disc {
	case string(api.TemplateDiscriminatorGitConfig):
		return t.handleGitConfig(ctx, configItem)
	case string(api.TemplateDiscriminatorKubernetesSec):
		return t.handleK8sConfig(configItem)
	case string(api.TemplateDiscriminatorInlineConfig):
		inlineSpec, err := configItem.AsInlineConfigProviderSpec()
		if err != nil {
			return fmt.Errorf("failed getting config item %s as InlineConfigProviderSpec: %w", inlineSpec.Name, err)
		}

		return t.handleInlineConfig(&inlineSpec)
	default:
		return fmt.Errorf("unsupported discriminator %s", disc)
	}
}

// Translate branch or tag into hash
func (t *TemplateVersionPopulateLogic) handleGitConfig(ctx context.Context, configItem *api.DeviceSpec_Config_Item) error {
	gitSpec, err := configItem.AsGitConfigProviderSpec()
	if err != nil {
		return fmt.Errorf("failed getting config item as GitConfigProviderSpec: %w", err)
	}

	repo, err := t.store.Repository().GetInternal(ctx, t.resourceRef.OrgID, gitSpec.GitRef.Repository)
	if err != nil {
		return fmt.Errorf("failed fetching specified Repository definition %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
	}

	mfs, hash, err := CloneGitRepo(repo, &gitSpec.GitRef.TargetRevision, util.IntToPtr(1))
	if err != nil {
		return fmt.Errorf("failed cloning specified git repository %s/%s: %w", t.resourceRef.OrgID, gitSpec.GitRef.Repository, err)
	}

	// Add this git config into the unrendered config, but with a hash for targetRevision
	gitSpec.GitRef.TargetRevision = hash
	newConfig := &api.TemplateVersionStatus_Config_Item{}
	err = newConfig.FromGitConfigProviderSpec(gitSpec)
	if err != nil {
		return fmt.Errorf("failed creating git config from item %s: %w", gitSpec.Name, err)
	}
	t.unrenderedConfig = append(t.unrenderedConfig, *newConfig)

	// Create an ignition from the git subtree and merge it into the rendered config
	ignitionConfig, err := t.getIgnitionFromFileSystem(mfs, gitSpec.GitRef.Path)
	if err != nil {
		return fmt.Errorf("failed parsing git config item %s: %w", gitSpec.Name, err)
	}
	renderedConfig := config_latest.Merge(*t.renderedConfig, *ignitionConfig)
	t.renderedConfig = &renderedConfig

	return nil
}

// TODO: implement
func (t *TemplateVersionPopulateLogic) handleK8sConfig(configItem *api.DeviceSpec_Config_Item) error {
	return fmt.Errorf("service does not yet support kubernetes config")
}

func (t *TemplateVersionPopulateLogic) handleInlineConfig(inlineSpec *api.InlineConfigProviderSpec) error {
	// Add this inline config into the unrendered config
	newConfig := &api.TemplateVersionStatus_Config_Item{}
	err := newConfig.FromInlineConfigProviderSpec(*inlineSpec)
	if err != nil {
		return fmt.Errorf("failed creating inline config from item %s: %w", inlineSpec.Name, err)
	}
	t.unrenderedConfig = append(t.unrenderedConfig, *newConfig)

	// Convert yaml to json
	yamlBytes, err := yaml.Marshal(inlineSpec.Inline)
	if err != nil {
		return fmt.Errorf("invalid yaml in inline config item %s: %w", inlineSpec.Name, err)
	}
	jsonBytes, err := yaml.YAMLToJSON(yamlBytes)
	if err != nil {
		return fmt.Errorf("failed converting yaml to json in inline config item %s: %w", inlineSpec.Name, err)
	}

	// Convert to ignition and merge into the rendered config
	ignitionConfig, _, err := config_latest.ParseCompatibleVersion(jsonBytes)
	if err != nil {
		return fmt.Errorf("failed parsing inline config item %s: %w", inlineSpec.Name, err)
	}
	renderedConfig := config_latest.Merge(*t.renderedConfig, ignitionConfig)
	t.renderedConfig = &renderedConfig

	return nil
}

func (t *TemplateVersionPopulateLogic) getIgnitionFromFileSystem(mfs billy.Filesystem, path string) (*config_latest_types.Config, error) {
	fileInfo, err := mfs.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("failed accessing path %s: %w", path, err)
	}
	ignitionConfig, _, _ := config_latest.ParseCompatibleVersion([]byte("{\"ignition\": {\"version\": \"3.4.0\"}"))

	if fileInfo.IsDir() {
		files, err := mfs.ReadDir(path)
		if err != nil {
			return nil, fmt.Errorf("failed reading directory %s: %w", path, err)
		}
		err = t.addGitDirToIgnitionConfig(mfs, path, "/", files, &ignitionConfig)
		if err != nil {
			return nil, fmt.Errorf("failed converting directory %s to ignition: %w", path, err)
		}
	} else {
		err = t.addGitFileToIgnitionConfig(mfs, path, "/", fileInfo, &ignitionConfig)
		if err != nil {
			return nil, fmt.Errorf("failed converting file %s to ignition: %w", path, err)
		}
	}

	return &ignitionConfig, nil
}

func (t *TemplateVersionPopulateLogic) addGitDirToIgnitionConfig(mfs billy.Filesystem, fullPrefix, ignPrefix string, fileInfos []fs.FileInfo, ignitionConfig *config_latest_types.Config) error {
	for _, fileInfo := range fileInfos {
		if fileInfo.IsDir() {
			subdirFiles, err := mfs.ReadDir(filepath.Join(fullPrefix, fileInfo.Name()))
			if err != nil {
				return fmt.Errorf("failed reading directory %s: %w", fileInfo.Name(), err)
			}
			// recursion!
			err = t.addGitDirToIgnitionConfig(mfs, filepath.Join(fullPrefix, fileInfo.Name()), filepath.Join(ignPrefix, fileInfo.Name()), subdirFiles, ignitionConfig)
			if err != nil {
				return err
			}
		} else {
			err := t.addGitFileToIgnitionConfig(mfs, filepath.Join(fullPrefix, fileInfo.Name()), filepath.Join(ignPrefix, fileInfo.Name()), fileInfo, ignitionConfig)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *TemplateVersionPopulateLogic) addGitFileToIgnitionConfig(mfs billy.Filesystem, fullPath, ignPath string, fileInfo fs.FileInfo, ignitionConfig *config_latest_types.Config) error {
	openFile, err := mfs.Open(fullPath)
	if err != nil {
		return err
	}
	defer openFile.Close()

	fileContents, err := io.ReadAll(openFile)
	if err != nil {
		return err
	}

	t.setFileInIgnition(ignitionConfig, ignPath, fileContents, int(fileInfo.Mode()), true)
	return nil
}

func (t *TemplateVersionPopulateLogic) setFileInIgnition(ignitionConfig *config_latest_types.Config, filePath string, fileBytes []byte, mode int, overwrite bool) {
	fileContents := "data:text/plain;charset=utf-8;base64," + base64.StdEncoding.EncodeToString(fileBytes)
	rootUser := "root"
	file := config_latest_types.File{
		Node: config_latest_types.Node{
			Path:      filePath,
			Overwrite: &overwrite,
			Group:     config_latest_types.NodeGroup{},
			User:      config_latest_types.NodeUser{Name: &rootUser},
		},
		FileEmbedded1: config_latest_types.FileEmbedded1{
			Append: []config_latest_types.Resource{},
			Contents: config_latest_types.Resource{
				Source: &fileContents,
			},
			Mode: &mode,
		},
	}
	ignitionConfig.Storage.Files = append(ignitionConfig.Storage.Files, file)
}

func (t *TemplateVersionPopulateLogic) setStatus(ctx context.Context, err error) error {
	t.templateVersion.Status = &api.TemplateVersionStatus{}
	if err != nil {
		t.log.Errorf("failed syncing template to template version: %v", err)
	} else {
		t.templateVersion.Status.Os = t.fleet.Spec.Template.Spec.Os
		t.templateVersion.Status.Containers = t.fleet.Spec.Template.Spec.Containers
		t.templateVersion.Status.Systemd = t.fleet.Spec.Template.Spec.Systemd
		t.templateVersion.Status.Config = &t.unrenderedConfig
	}
	t.templateVersion.Status.Conditions = &[]api.Condition{}
	api.SetStatusConditionByError(t.templateVersion.Status.Conditions, api.TemplateVersionValid, "Valid", "Invalid", err)

	var config *string
	if err == nil {
		configjson, err := json.Marshal(t.renderedConfig)
		if err != nil {
			return fmt.Errorf("failed to marshal rendered config: %w", err)
		}
		configStr := string(configjson)
		config = &configStr
	}

	return t.store.TemplateVersion().UpdateStatusAndConfig(ctx, t.resourceRef.OrgID, t.templateVersion, util.BoolToPtr(err == nil), config, t.taskManager.TemplateVersionValidatedCallback)
}
