package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/go-git/go-billy/v5"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

const ResourceSyncTaskName = "resourcesync"

type ResourceSync struct {
	log                   logrus.FieldLogger
	serviceHandler        service.Service
	ignoreResourceUpdates []string
}

type GenericResourceMap map[string]interface{}

var validFileExtensions = []string{"json", "yaml", "yml"}
var supportedResources = []string{api.FleetKind}

func NewResourceSync(serviceHandler service.Service, log logrus.FieldLogger, ignoreResourceUpdates []string) *ResourceSync {
	return &ResourceSync{
		log:                   log,
		serviceHandler:        serviceHandler,
		ignoreResourceUpdates: ignoreResourceUpdates,
	}
}

func (r *ResourceSync) Poll(ctx context.Context, orgId uuid.UUID) {
	log := log.WithReqIDFromCtx(ctx, r.log)

	log.Info("Running ResourceSync Polling")

	limit := int32(ItemsPerPage)
	continueToken := (*string)(nil)

	for {
		resourcesyncs, status := r.serviceHandler.ListResourceSyncs(ctx, orgId, api.ListResourceSyncsParams{
			Limit:    &limit,
			Continue: continueToken,
		})
		if status.Code != 200 {
			log.Errorf("error fetching repositories: %s", status.Message)
			return
		}

		for i := range resourcesyncs.Items {
			rs := &resourcesyncs.Items[i]
			err := r.run(ctx, log, orgId, rs)
			if err != nil {
				log.Errorf("resourcesync/%s: error during run: %v", *rs.Metadata.Name, err)
			}
		}

		continueToken = resourcesyncs.Metadata.Continue
		if continueToken == nil {
			break
		}
	}
}

func (r *ResourceSync) run(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID, rs *api.ResourceSync) error {
	resourceName := lo.FromPtr(rs.Metadata.Name)
	defer r.updateResourceSyncStatus(ctx, orgId, rs)

	// Get repository and validate accessibility
	repo, err := r.GetRepositoryAndValidateAccess(ctx, orgId, rs)
	if err != nil {
		return err
	}

	// Parse and validate resources
	resources, err := r.parseAndValidateResources(rs, repo, CloneGitRepo)
	if err != nil {
		log.Errorf("resourcesync/%s: parsing failed. error: %s", *rs.Metadata.Name, err.Error())
		return err
	}
	if resources == nil {
		// No resources to sync
		return nil
	}

	// Parse fleets from resources
	fleets, err := r.ParseFleetsFromResources(resources, resourceName)

	// Set the ResourceParsed condition based on the result
	api.SetStatusConditionByError(&rs.Status.Conditions, api.ConditionTypeResourceSyncResourceParsed, "success", "fail", err)

	if err != nil {
		log.Errorf("%v", err)
		return err
	}

	// Sync fleets
	return r.SyncFleets(ctx, log, orgId, rs, fleets, resourceName)
}

// GetRepositoryAndValidateAccess gets the repository and validates it's accessible
func (r *ResourceSync) GetRepositoryAndValidateAccess(ctx context.Context, orgId uuid.UUID, rs *api.ResourceSync) (*api.Repository, error) {
	if rs == nil {
		return nil, fmt.Errorf("ResourceSync is nil")
	}

	repoName := rs.Spec.Repository
	repo, status := r.serviceHandler.GetRepository(ctx, orgId, repoName)
	err := service.ApiStatusToErr(status)

	// Ensure Status and Conditions are initialized
	if rs.Status == nil {
		rs.Status = &api.ResourceSyncStatus{}
	}
	if rs.Status.Conditions == nil {
		rs.Status.Conditions = []api.Condition{}
	}

	api.SetStatusConditionByError(&rs.Status.Conditions, api.ConditionTypeResourceSyncAccessible, "accessible", "repository resource not found", err)
	if err != nil {
		return nil, err
	}
	return repo, nil
}

// ParseFleetsFromResources parses fleets from generic resources
func (r *ResourceSync) ParseFleetsFromResources(resources []GenericResourceMap, resourceName string) ([]*api.Fleet, error) {
	fleets, err := r.parseFleets(resources)
	// Note: We can't set conditions here since we don't have access to rs
	// The conditions will be set in the calling method
	if err != nil {
		err = fmt.Errorf("resource %s: error: %w", resourceName, err)
		return nil, err
	}
	return fleets, nil
}

// SyncFleets syncs the fleets to the service
func (r *ResourceSync) SyncFleets(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID, rs *api.ResourceSync, fleets []*api.Fleet, resourceName string) error {
	if rs == nil {
		return fmt.Errorf("ResourceSync is nil")
	}

	// Ensure Status and Conditions are initialized
	if rs.Status == nil {
		rs.Status = &api.ResourceSyncStatus{}
	}
	if rs.Status.Conditions == nil {
		rs.Status.Conditions = []api.Condition{}
	}

	owner := util.SetResourceOwner(api.ResourceSyncKind, resourceName)

	// Validate that no fleet names conflict with fleets owned by other ResourceSyncs
	err := r.validateFleetNameConflicts(ctx, orgId, fleets, *owner)
	if err != nil {
		err = fmt.Errorf("resource %s: error: %w", resourceName, err)
		log.Errorf("%v", err)
		api.SetStatusConditionByError(&rs.Status.Conditions, api.ConditionTypeResourceSyncSynced, "success", "fail", err)
		return err
	}

	fleetsPreOwned := make([]api.Fleet, 0)

	listParams := api.ListFleetsParams{
		Limit:         lo.ToPtr(int32(100)),
		FieldSelector: lo.ToPtr(fmt.Sprintf("metadata.owner=%s", *owner)),
	}
	for {
		var listRes *api.FleetList
		var status api.Status
		listRes, status = r.serviceHandler.ListFleets(ctx, orgId, listParams)
		if status.Code != http.StatusOK {
			err = fmt.Errorf("resource %s: failed to list owned fleets. error: %s", resourceName, status.Message)
			log.Errorf("%v", err)
			api.SetStatusConditionByError(&rs.Status.Conditions, api.ConditionTypeResourceSyncSynced, "success", "fail", err)
			return err
		}
		fleetsPreOwned = append(fleetsPreOwned, listRes.Items...)
		if listRes.Metadata.Continue == nil {
			break
		}
		listParams.Continue = listRes.Metadata.Continue
	}

	fleetsToRemove := fleetsDelta(fleetsPreOwned, fleets)

	log.Infof("Resource %s: applying %d fleets ", resourceName, len(fleets))
	createUpdateErr := r.createOrUpdateMultiple(ctx, orgId, owner, fleets...)
	if errors.Is(createUpdateErr, flterrors.ErrUpdatingResourceWithOwnerNotAllowed) {
		log.Errorf("one or more fleets are managed by a different resource. %v", createUpdateErr)
	}
	if len(fleetsToRemove) > 0 {
		log.Infof("Resource %s: found #%d fleets to remove. removing\n", resourceName, len(fleetsToRemove))
		for _, fleetToRemove := range fleetsToRemove {
			status := r.serviceHandler.DeleteFleet(ctx, orgId, fleetToRemove)
			if status.Code != http.StatusOK {
				err := fmt.Errorf("resource %s: failed to remove old fleet %s. error: %s", resourceName, fleetToRemove, status.Message)
				log.Errorf("%v", err)
				api.SetStatusConditionByError(&rs.Status.Conditions, api.ConditionTypeResourceSyncSynced, "success", "fail", err)
				return service.ApiStatusToErr(status)
			}
		}
	}
	api.SetStatusConditionByError(&rs.Status.Conditions, api.ConditionTypeResourceSyncSynced, "success", "fail", createUpdateErr)
	if createUpdateErr != nil {
		log.Errorf("Resource %s: failed to apply resource. error: %s", resourceName, createUpdateErr.Error())
		return createUpdateErr
	}
	rs.Status.ObservedGeneration = rs.Metadata.Generation
	log.Infof("Resource %s: %d fleets applied successfully\n", resourceName, len(fleets))
	return createUpdateErr
}

func (r *ResourceSync) createOrUpdateMultiple(ctx context.Context, orgId uuid.UUID, owner *string, resources ...*api.Fleet) error {
	var errs []error
	for _, resource := range resources {
		// Create a context where InternalRequestCtxKey is false so that ReplaceFleet
		// treats this as an external API request and calls NilOutManagedObjectMetaProperties,
		// which will nil out annotations. This ensures annotations are not updated by ResourceSync.
		// Annotations are managed by the service (e.g., fleet-controller/templateVersion)
		// and should not be overwritten when syncing from YAML.
		// Set ResourceSyncRequestCtxKey to allow resource sync to update resources it owns.
		externalCtx := context.WithValue(ctx, consts.InternalRequestCtxKey, false)
		externalCtx = context.WithValue(externalCtx, consts.ResourceSyncRequestCtxKey, true)
		updatedFleet, status := r.serviceHandler.ReplaceFleet(externalCtx, orgId, *resource.Metadata.Name, *resource)
		if status.Code != http.StatusOK && status.Code != http.StatusCreated {
			if status.Message == flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error() {
				errs = append(errs, errors.New("one or more fleets are managed by a different resource"))
			} else {
				errs = append(errs, service.ApiStatusToErr(status))
			}
		}

		// Update the owner of the fleet if not already set
		if updatedFleet != nil && util.DefaultIfNil(updatedFleet.Metadata.Owner, "") != util.DefaultIfNil(owner, "") {
			updatedFleet.Metadata.Owner = owner
			_, status := r.serviceHandler.ReplaceFleet(ctx, orgId, *resource.Metadata.Name, *updatedFleet)
			if status.Code != http.StatusOK {
				errs = append(errs, service.ApiStatusToErr(status))
			}
		}
	}
	return errors.Join(lo.Uniq(errs)...)
}

// Returns a list of names that are no longer present
func fleetsDelta(owned []api.Fleet, newOwned []*api.Fleet) []string {
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

// NeedsSyncToHash returns true if the resource needs to be synced to the given hash.
func NeedsSyncToHash(rs *api.ResourceSync, hash string) bool {
	if rs.Status == nil || rs.Status.Conditions == nil {
		return true
	}

	if api.IsStatusConditionFalse(rs.Status.Conditions, api.ConditionTypeResourceSyncSynced) {
		return true
	}

	var observedGen int64 = 0
	if rs.Status.ObservedGeneration != nil {
		observedGen = *rs.Status.ObservedGeneration
	}
	var prevHash = util.DefaultIfNil(rs.Status.ObservedCommit, "")
	return hash != prevHash || observedGen != *rs.Metadata.Generation
}

func (r *ResourceSync) parseAndValidateResources(rs *api.ResourceSync, repo *api.Repository, gitCloneRepo cloneGitRepoFunc) ([]GenericResourceMap, error) {
	path := rs.Spec.Path
	revision := rs.Spec.TargetRevision
	mfs, hash, err := gitCloneRepo(repo, &revision, lo.ToPtr(1))
	api.SetStatusConditionByError(&rs.Status.Conditions, api.ConditionTypeResourceSyncAccessible, "accessible", "failed to clone repository", err)
	if err != nil {
		return nil, err
	}

	if !NeedsSyncToHash(rs, hash) {
		// nothing to update
		r.log.Infof("resourcesync/%s: No new commits or path. skipping", *rs.Metadata.Name)
		return nil, nil
	}

	// Set Synced condition to False with reason indicating new hash detected
	// This is not a failure, just indicates we're out of sync and need to sync
	api.SetStatusConditionByError(&rs.Status.Conditions, api.ConditionTypeResourceSyncSynced, "success", api.ResourceSyncNewHashDetectedReason, fmt.Errorf("detected new hash %s", hash))

	rs.Status.ObservedCommit = lo.ToPtr(hash)

	// Open files
	fileInfo, err := mfs.Stat(path)
	api.SetStatusConditionByError(&rs.Status.Conditions, api.ConditionTypeResourceSyncAccessible, "accessible", "path not found in repository", err)
	if err != nil {
		return nil, err
	}
	var resources []GenericResourceMap
	if fileInfo.IsDir() {
		resources, err = r.extractResourcesFromDir(mfs, path)
	} else {
		resources, err = r.extractResourcesFromFile(mfs, path)
	}
	api.SetStatusConditionByError(&rs.Status.Conditions, api.ConditionTypeResourceSyncResourceParsed, "success", "fail", err)
	if err != nil {
		return nil, err

	}
	return resources, nil
}

func (r *ResourceSync) extractResourcesFromDir(mfs billy.Filesystem, path string) ([]GenericResourceMap, error) {
	genericResources := []GenericResourceMap{}
	files, err := mfs.ReadDir(path)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if !file.IsDir() && isValidFile(file.Name()) { // Not going recursively into subfolders
			resources, err := r.extractResourcesFromFile(mfs, mfs.Join(path, file.Name()))
			if err != nil {
				return nil, err
			}
			genericResources = append(genericResources, resources...)
		}
	}
	return genericResources, nil
}

func (r *ResourceSync) extractResourcesFromFile(mfs billy.Filesystem, path string) ([]GenericResourceMap, error) {
	genericResources := []GenericResourceMap{}

	file, err := mfs.Open(path)
	if err != nil {
		// Failed to open file....
		return nil, err
	}
	defer file.Close()
	decoder := yamlutil.NewYAMLOrJSONDecoder(file, 100)

	for {
		var resource GenericResourceMap
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

		resource = RemoveIgnoredFields(resource, r.ignoreResourceUpdates)
		genericResources = append(genericResources, resource)
	}
	if !errors.Is(err, io.EOF) {
		return nil, err
	}
	return genericResources, nil

}

func RemoveIgnoredFields(resource GenericResourceMap, ignorePaths []string) GenericResourceMap {
	for _, path := range ignorePaths {
		parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
		removeFieldFromMap(resource, parts)
	}
	return resource
}

func removeFieldFromMap(m map[string]interface{}, parts []string) {
	if len(parts) == 0 {
		return
	}

	if len(parts) == 1 {
		delete(m, parts[0])
		return
	}

	next, ok := m[parts[0]]
	if !ok {
		return
	}

	// Try to convert next to map[string]interface{} for nested removal
	if nextMap, ok := next.(map[string]interface{}); ok {
		removeFieldFromMap(nextMap, parts[1:])
	}
}

func (r ResourceSync) parseFleets(resources []GenericResourceMap) ([]*api.Fleet, error) {
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
		case api.FleetKind:
			var fleet api.Fleet
			err := yamlutil.Unmarshal(buf, &fleet)
			if err != nil {
				return nil, fmt.Errorf("decoding Fleet resource: %w", err)
			}
			if fleet.Metadata.Name == nil {
				return nil, fmt.Errorf("decoding Fleet resource: missing field .metadata.name: %w", err)
			}
			if errs := fleet.Validate(); len(errs) > 0 {
				return nil, fmt.Errorf("failed validating fleet %s: %w", *fleet.Metadata.Name, errors.Join(errs...))
			}
			name, nameExists := names[*fleet.Metadata.Name]
			if nameExists {
				return nil, fmt.Errorf("found multiple fleet definitions with name '%s'", name)
			}
			names[name] = name
			fleets = append(fleets, &fleet)
		default:
			return nil, fmt.Errorf("resource of unknown/unsupported kind %q: %v", kind, resource)
		}
	}

	return fleets, nil
}

func (r *ResourceSync) updateResourceSyncStatus(ctx context.Context, orgId uuid.UUID, rs *api.ResourceSync) {
	_, status := r.serviceHandler.ReplaceResourceSyncStatus(ctx, orgId, *rs.Metadata.Name, *rs)
	if status.Code != http.StatusOK {
		r.log.Errorf("Failed to update resourcesync status for %s: %s", *rs.Metadata.Name, status.Message)
	}
}

func isValidFile(filename string) bool {
	ext := ""
	splits := strings.Split(filename, ".")
	if len(splits) > 0 {
		ext = splits[len(splits)-1]
	}
	for _, validExt := range validFileExtensions {
		if ext == validExt {
			return true
		}
	}
	return false
}

func (r *ResourceSync) validateFleetNameConflicts(ctx context.Context, orgId uuid.UUID, fleets []*api.Fleet, owner string) error {
	var conflictingFleets []string

	for _, fleet := range fleets {
		fleetName := *fleet.Metadata.Name
		// Check if a fleet with this name already exists
		existingFleet, status := r.serviceHandler.GetFleet(ctx, orgId, fleetName, api.GetFleetParams{})
		if status.Code == http.StatusOK {
			// Fleet exists - check if it's owned by a different ResourceSync
			if existingFleet.Metadata.Owner != nil && *existingFleet.Metadata.Owner != owner {
				conflictingFleets = append(conflictingFleets, fleetName)
			}
		} else if status.Code != http.StatusNotFound {
			return fmt.Errorf("failed to check existing fleet '%s': %s", fleetName, status.Message)
		}
		// If status is 404 (not found), no conflict - fleet can be created
	}

	if len(conflictingFleets) > 0 {
		return fmt.Errorf("fleet name(s) %v conflict with existing fleets managed by different ResourceSyncs", conflictingFleets)
	}

	return nil
}
