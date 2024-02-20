package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	gitplumbing "github.com/go-git/go-git/v5/plumbing"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitmemory "github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

type ResourceSync struct {
	log         logrus.FieldLogger
	store       store.Store
	taskManager TaskManager
}

type genericResourceMap map[string]interface{}

var fileExtensions = []string{"json", "yaml", "yml"}
var supportedResources = []string{model.FleetKind}

func NewResourceSync(taskManager TaskManager) *ResourceSync {
	return &ResourceSync{
		log:         taskManager.log,
		store:       taskManager.store,
		taskManager: taskManager,
	}
}

func (r *ResourceSync) Poll() {
	reqid.OverridePrefix("resourcesync")
	requestID := reqid.NextRequestID()
	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, requestID)
	log := log.WithReqIDFromCtx(ctx, r.log)

	log.Info("Running ResourceSync Polling")

	resourcesyncs, err := r.store.ResourceSync().ListIgnoreOrg()
	if err != nil {
		log.Errorf("error fetching resourcesyncs: %s", err)
		return
	}

	for i := range resourcesyncs {
		rs := &resourcesyncs[i]
		_ = r.run(ctx, log, rs)
	}
}

func (r *ResourceSync) run(ctx context.Context, log logrus.FieldLogger, rs *model.ResourceSync) error {
	defer r.updateResourceSyncStatus(rs)
	reponame := rs.Spec.Data.Repository
	repo, err := r.store.Repository().GetInternal(ctx, rs.OrgID, *reponame)
	if err != nil {
		// Failed to fetch Repository resource
		addRepoNotFoundCondition(rs, err)
		return err
	}
	addRepoNotFoundCondition(rs, nil)
	resources, err := r.parseAndValidateResources(ctx, rs, repo)
	if err != nil {
		log.Errorf("resourcesync/%s: parsing failed. error: %s", rs.Name, err.Error())
		return err
	}

	owner := util.SetResourceOwner(model.ResourceSyncKind, rs.Name)
	fleets, err := r.parseFleets(resources, rs.OrgID, owner)
	if err != nil {
		err := fmt.Errorf("resourcesync/%s: error: %w", rs.Name, err)
		log.Errorf("%e", err)
		addResourceParsedCondition(rs, err)
		return err
	}
	addResourceParsedCondition(rs, nil)

	fleetsOwned := make([]api.Fleet, 0)

	listParams := store.ListParams{
		Owner: owner,
		Limit: 100,
	}
	for {
		listRes, err := r.store.Fleet().List(ctx, rs.OrgID, listParams)
		if err != nil {
			err := fmt.Errorf("resourcesync/%s: failed to list owned fleets. error: %w", rs.Name, err)
			log.Errorf("%e", err)
			return err
		}
		fleetsOwned = append(fleetsOwned, listRes.Items...)
		if listRes.Metadata.Continue == nil {
			break
		}
		cont, err := store.ParseContinueString(listRes.Metadata.Continue)
		if err != nil {
			return fmt.Errorf("resourcesync/%s: failed to parse continuation for paging: %w", rs.Name, err)
		}
		listParams.Continue = cont
	}

	fleetsToRemove := r.fleetsDelta(fleetsOwned, fleets)

	r.log.Infof("resourcesync/%s: applying #%d fleets ", rs.Name, len(fleets))
	err = r.store.Fleet().CreateOrUpdateMultiple(ctx, rs.OrgID, r.taskManager.FleetTemplateRolloutCallback, fleets...)
	if err == gorm.ErrInvalidData {
		err = fmt.Errorf("one or more fleets are managed by a differen resource. %w", err)
	}
	if len(fleetsToRemove) > 0 {
		r.log.Infof("resourcesync/%s: found #%d fleets to remove. removing\n", rs.Name, len(fleets))
		err := r.store.Fleet().Delete(ctx, rs.OrgID, fleetsToRemove...)
		if err != nil {
			log.Errorf("resourcesync/%s: failed to remove old fleets. error: %s", rs.Name, err.Error())
			return err
		}

	}
	addSyncedCondition(rs, err)
	if err != nil {
		log.Errorf("resourcesync/%s: failed to apply resource. error: %s", rs.Name, err.Error())
		return err
	}
	rs.Status.Data.ObservedGeneration = rs.Generation
	r.log.Infof("resourcesync/%s: #%d fleets applied successfully\n", rs.Name, len(fleets))
	return nil
}

// Returns a list of names that are no longer present
func (r *ResourceSync) fleetsDelta(owned []api.Fleet, newOwned []*api.Fleet) []string {
	dfleets := make([]string, 0)
	for _, ownedFleet := range owned {
		found := false
		name := ownedFleet.Metadata.Name
		for _, newFleet := range newOwned {
			if *name == *newFleet.Metadata.Name {
				found = true
				break
			}
		}
		if !found {
			dfleets = append(dfleets, *name)
		}
	}

	return dfleets
}

func (r *ResourceSync) parseAndValidateResources(ctx context.Context, rs *model.ResourceSync, repo *model.Repository) ([]genericResourceMap, error) {
	path := *rs.Spec.Data.Path
	revision := rs.Spec.Data.TargetRevision
	mfs, hash, err := r.cloneRepo(repo, revision)
	if err != nil {
		// Cant fetch git repo
		addRepoAccessCondition(rs, err)
		return nil, err
	}
	addRepoAccessCondition(rs, nil)

	if !shouldRunSync(hash, *rs) {
		// nothing to update
		r.log.Infof("resourcesync/%s: No new commits or path. skipping", rs.Name)
		return nil, nil
	}

	rs.Status.Data.ObservedCommit = util.StrToPtr(hash)

	// Open files
	fileInfo, err := mfs.Stat(path)
	if err != nil {
		// Cant fetch git repo
		addPathAccessCondition(rs, err)
		return nil, err
	}
	addPathAccessCondition(rs, nil)
	var resources []genericResourceMap
	if fileInfo.IsDir() {
		resources, err = r.extractResourcesFromDir(rs.OrgID.String(), mfs, path)
	} else {
		resources, err = r.extractResourcesFromFile(rs.OrgID.String(), mfs, path)
	}
	if err != nil {
		// Failed to parse resources
		addResourceParsedCondition(rs, err)
		return nil, err

	}
	addResourceParsedCondition(rs, nil)
	return resources, nil
}

func (r *ResourceSync) cloneRepo(repo *model.Repository, revision *string) (billy.Filesystem, string, error) {
	storage := gitmemory.NewStorage()
	mfs := memfs.New()
	ops := &git.CloneOptions{
		URL:   *repo.Spec.Data.Repo,
		Depth: 1,
	}
	if repo.Spec.Data.Username != nil && repo.Spec.Data.Password != nil {
		ops.Auth = &githttp.BasicAuth{
			Username: *repo.Spec.Data.Username,
			Password: *repo.Spec.Data.Password,
		}
	}
	if revision != nil {
		ops.ReferenceName = gitplumbing.ReferenceName(*revision)
	}
	gitRepo, err := git.Clone(storage, mfs, ops)
	if err != nil {
		return nil, "", err
	}
	head, err := gitRepo.Head()
	if err != nil {
		return nil, "", err
	}
	hash := head.Hash().String()
	return mfs, hash, nil
}

func (r *ResourceSync) extractResourcesFromDir(orgId string, mfs billy.Filesystem, path string) ([]genericResourceMap, error) {
	genericResources := []genericResourceMap{}
	files, err := mfs.ReadDir(path)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if !file.IsDir() && isValidFile(file.Name()) { // Not going recursivly into subfolders
			resources, err := r.extractResourcesFromFile(orgId, mfs, mfs.Join(path, file.Name()))
			if err != nil {
				return nil, err
			}
			genericResources = append(genericResources, resources...)
		}
	}
	return genericResources, nil
}

func (r *ResourceSync) extractResourcesFromFile(orgId string, mfs billy.Filesystem, path string) ([]genericResourceMap, error) {
	genericResources := []genericResourceMap{}

	file, err := mfs.Open(path)
	if err != nil {
		// Failed to open file....
		return nil, err
	}
	defer file.Close()
	decoder := yamlutil.NewYAMLOrJSONDecoder(file, 100)

	for {
		var resource genericResourceMap
		err = decoder.Decode(&resource)
		if err != nil {
			break
		}
		kind, kindok := resource["kind"].(string)
		meta, metaok := resource["metadata"].(map[string]interface{})
		if !kindok || !metaok {
			return nil, fmt.Errorf("invalid resource definition at '%s'", path)
		}
		_, nameok := meta["name"].(string)
		if !nameok {
			return nil, fmt.Errorf("invalid resource definition at '%s'. resource name missing", path)
		}
		isSupportedResource := false
		for _, supportedResource := range supportedResources {
			if kind == supportedResource {
				isSupportedResource = true
				break
			}
		}
		if !isSupportedResource {
			return nil, fmt.Errorf("invalid resource type at '%s'. unsupported kind '%s'", path, kind)
		}
		genericResources = append(genericResources, resource)
	}
	if !errors.Is(err, io.EOF) {
		return nil, err
	}
	return genericResources, nil

}

func (r ResourceSync) parseFleets(resources []genericResourceMap, orgId uuid.UUID, owner *string) ([]*api.Fleet, error) {
	fleets := make([]*api.Fleet, 0)
	names := make(map[string]string)
	for _, resource := range resources {
		kind, ok := resource["kind"].(string)
		if !ok {
			return nil, fmt.Errorf("resource with unspecified kind: %v", resource)
		}
		buf, err := json.Marshal(resource)
		if err != nil {
			return nil, fmt.Errorf("failed to parse generic resource: %w", err)
		}

		switch kind {
		case model.FleetKind:
			var fleet api.Fleet
			err := yamlutil.Unmarshal(buf, &fleet)
			if err != nil {
				return nil, fmt.Errorf("decoding Fleet resource: %w", err)
			}
			if fleet.Metadata.Name == nil {
				return nil, fmt.Errorf("decoding Fleet resource: missing field .metadata.name: %w", err)
			}
			name, nameExists := names[*fleet.Metadata.Name]
			if nameExists {
				return nil, fmt.Errorf("found multiple fleet definitions with name '%s'", name)
			}
			names[name] = name

			fleet.Metadata.Owner = owner

			fleets = append(fleets, &fleet)
		default:
			return nil, fmt.Errorf("resource of unknown/unsupported kind %q: %v", kind, resource)
		}
	}

	return fleets, nil
}

func (r *ResourceSync) updateResourceSyncStatus(rs *model.ResourceSync) {
	err := r.store.ResourceSync().UpdateStatusIgnoreOrg(rs)
	if err != nil {
		r.log.Errorf("Failed to update resourcesync status for %s: %v", rs.Name, err)
	}
}

func shouldRunSync(hash string, rs model.ResourceSync) bool {
	if rs.Status == nil || rs.Status.Data.Conditions == nil {
		return true
	}

	if api.IsStatusConditionFalse(*rs.Status.Data.Conditions, api.ResourceSyncSynced) {
		return true
	}

	var observedGen int64 = 0
	if rs.Status.Data.ObservedGeneration != nil {
		observedGen = *rs.Status.Data.ObservedGeneration
	}
	var prevHash string = util.DefaultIfNil(rs.Status.Data.ObservedCommit, "")
	return hash != prevHash || observedGen != *rs.Generation
}

func isValidFile(filename string) bool {
	ext := ""
	splits := strings.Split(filename, ".")
	if len(splits) > 0 {
		ext = splits[len(splits)-1]
	}
	for _, validExt := range fileExtensions {
		if ext == validExt {
			return true
		}
	}
	return false
}

func ensureConditionsNotNil(resSync *model.ResourceSync) {
	if resSync.Status == nil {
		resSync.Status = &model.JSONField[api.ResourceSyncStatus]{
			Data: api.ResourceSyncStatus{
				Conditions: &[]api.Condition{},
			},
		}
	}
	if resSync.Status.Data.Conditions == nil {
		resSync.Status.Data.Conditions = &[]api.Condition{}
	}
}

func addRepoNotFoundCondition(resSync *model.ResourceSync, err error) {
	ensureConditionsNotNil(resSync)
	api.SetStatusConditionByError(resSync.Status.Data.Conditions, api.ResourceSyncAccessible, "accessible", "repository resource not found", err)
}

func addRepoAccessCondition(resSync *model.ResourceSync, err error) {
	ensureConditionsNotNil(resSync)
	api.SetStatusConditionByError(resSync.Status.Data.Conditions, api.ResourceSyncAccessible, "accessible", "failed to clone repository", err)
}

func addPathAccessCondition(resSync *model.ResourceSync, err error) {
	ensureConditionsNotNil(resSync)
	api.SetStatusConditionByError(resSync.Status.Data.Conditions, api.ResourceSyncAccessible, "accessible", "path not found in repository", err)
}

func addResourceParsedCondition(resSync *model.ResourceSync, err error) {
	ensureConditionsNotNil(resSync)
	api.SetStatusConditionByError(resSync.Status.Data.Conditions, api.ResourceSyncResourceParsed, "Success", "Fail", err)
}

func addSyncedCondition(resSync *model.ResourceSync, err error) {
	ensureConditionsNotNil(resSync)
	api.SetStatusConditionByError(resSync.Status.Data.Conditions, api.ResourceSyncSynced, "Success", "Fail", err)
}
