package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/go-git/go-billy/v5"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

const ResourceSyncTaskName = "resourcesync"

type ResourceSync struct {
	log             logrus.FieldLogger
	serviceHandler  service.Service
	callbackManager tasks_client.CallbackManager
}

type genericResourceMap map[string]interface{}

var validFileExtensions = []string{"json", "yaml", "yml"}
var supportedResources = []string{api.FleetKind}

func NewResourceSync(callbackManager tasks_client.CallbackManager, serviceHandler service.Service, log logrus.FieldLogger) *ResourceSync {
	return &ResourceSync{
		log:             log,
		serviceHandler:  serviceHandler,
		callbackManager: callbackManager,
	}
}

func (r *ResourceSync) Poll(ctx context.Context) {
	log := log.WithReqIDFromCtx(ctx, r.log)

	log.Info("Running ResourceSync Polling")

	limit := int32(ItemsPerPage)
	continueToken := (*string)(nil)

	for {
		resourcesyncs, status := r.serviceHandler.ListResourceSyncs(ctx, api.ListResourceSyncsParams{
			Limit:    &limit,
			Continue: continueToken,
		})
		if status.Code != 200 {
			log.Errorf("error fetching repositories: %s", status.Message)
			return
		}

		for i := range resourcesyncs.Items {
			rs := &resourcesyncs.Items[i]
			details, errorMessages, err := r.run(ctx, log, rs)
			if err != nil {
				log.Errorf("resourcesync/%s: error during run: %v", *rs.Metadata.Name, err)
			}
			r.sendEvent(ctx, rs.Metadata.Name, details, errorMessages)
		}

		continueToken = resourcesyncs.Metadata.Continue
		if continueToken == nil {
			break
		}
	}
}

func (r *ResourceSync) sendEvent(ctx context.Context, rsName *string, details api.ResourceSyncCompletedDetails, errorMessages []string) {
	if r.serviceHandler == nil || details.ChangeCount == 0 {
		return
	}
	r.serviceHandler.CreateEvent(ctx, service.GetResourceSyncTaskEvent(ctx, rsName, details, errorMessages, r.log))
}

func makeResourceSyncCompletedDetails(commitHash string, totalChanges int, errorMessages []string) api.ResourceSyncCompletedDetails {
	return api.ResourceSyncCompletedDetails{
		CommitHash:  commitHash,
		ChangeCount: totalChanges,
		ErrorCount:  len(errorMessages),
	}
}

func (r *ResourceSync) run(ctx context.Context, log logrus.FieldLogger, rs *api.ResourceSync) (api.ResourceSyncCompletedDetails, []string, error) {
	resourceName := lo.FromPtr(rs.Metadata.Name)
	defer r.updateResourceSyncStatus(ctx, rs)
	repoName := rs.Spec.Repository
	repo, status := r.serviceHandler.GetRepository(ctx, repoName)
	err := service.ApiStatusToErr(status)
	api.SetStatusConditionByError(&rs.Status.Conditions, api.ConditionTypeResourceSyncAccessible, "accessible", "repository resource not found", err)
	if err != nil {
		return makeResourceSyncCompletedDetails("", 0, []string{"repository resource not found"}), []string{"repository resource not found"}, err
	}
	resources, commitHash, err := r.parseAndValidateResources(rs, repo, CloneGitRepo)
	if err != nil {
		log.Errorf("resourcesync/%s: parsing failed. error: %s", *rs.Metadata.Name, err.Error())
		log.Errorf("resource %s: parsing failed. error: %s", resourceName, err.Error())
		return makeResourceSyncCompletedDetails("", 0, []string{"parsing failed"}), []string{"parsing failed"}, err
	}
	if resources == nil {
		// No resources to sync
		return api.ResourceSyncCompletedDetails{}, []string{}, nil
	}

	totalChanges := len(resources)
	owner := util.SetResourceOwner(api.ResourceSyncKind, resourceName)
	fleets, err := r.parseFleets(resources, owner)
	api.SetStatusConditionByError(&rs.Status.Conditions, api.ConditionTypeResourceSyncResourceParsed, "success", "fail", err)
	if err != nil {
		err = fmt.Errorf("resource %s: error: %w", resourceName, err)
		log.Errorf("%e", err)
		return makeResourceSyncCompletedDetails(commitHash, totalChanges, []string{"parsing fleets failed"}), []string{"parsing fleets failed"}, err
	}

	// Validate that no fleet names conflict with fleets owned by other ResourceSyncs
	err = r.validateFleetNameConflicts(ctx, fleets, *owner)
	if err != nil {
		err = fmt.Errorf("resource %s: error: %w", resourceName, err)
		log.Errorf("%e", err)
		return makeResourceSyncCompletedDetails(commitHash, totalChanges, []string{"fleet name validation failed"}), []string{"fleet name validation failed"}, err
	}

	fleetsPreOwned := make([]api.Fleet, 0)

	listParams := api.ListFleetsParams{
		Limit:         lo.ToPtr(int32(100)),
		FieldSelector: lo.ToPtr(fmt.Sprintf("metadata.owner=%s", *owner)),
	}
	for {
		var listRes *api.FleetList
		listRes, status = r.serviceHandler.ListFleets(ctx, listParams)
		if status.Code != http.StatusOK {
			err = fmt.Errorf("resource %s: failed to list owned fleets. error: %s", resourceName, status.Message)
			log.Errorf("%e", err)
			listingError := []string{fmt.Sprintf("listing owned fleets failed: %v", err)}
			return makeResourceSyncCompletedDetails(commitHash, totalChanges, listingError), listingError, err
		}
		fleetsPreOwned = append(fleetsPreOwned, listRes.Items...)
		if listRes.Metadata.Continue == nil {
			break
		}
		listParams.Continue = listRes.Metadata.Continue
	}

	fleetsToRemove := fleetsDelta(fleetsPreOwned, fleets)
	// +2 for:
	// - possible error ErrUpdatingResourceWithOwnerNotAllowed
	// - possible error createUpdateErr
	errorMessages := make([]string, 0, len(fleetsToRemove)+2)

	log.Infof("Resource %s: applying %d fleets ", resourceName, len(fleets))
	createUpdateErr := r.createOrUpdateMultiple(ctx, fleets...)
	if errors.Is(createUpdateErr, flterrors.ErrUpdatingResourceWithOwnerNotAllowed) {
		log.Errorf("one or more fleets are managed by a different resource. %v", createUpdateErr)
		errorMessages = append(errorMessages, "one or more fleets are managed by a different resource")
	}
	if len(fleetsToRemove) > 0 {
		log.Infof("Resource %s: found #%d fleets to remove. removing\n", resourceName, len(fleetsToRemove))
		errorMessages = append(errorMessages, fmt.Sprintf("found %d fleets to remove", len(fleetsToRemove)))
		for _, fleetToRemove := range fleetsToRemove {
			status = r.serviceHandler.DeleteFleet(ctx, fleetToRemove)
			if status.Code != http.StatusOK {
				log.Errorf("Resource %s: failed to remove old fleet %s. error: %s", resourceName, fleetToRemove, status.Message)
				errorMessages = append(errorMessages, fmt.Sprintf("failed to remove old fleet %s: %s", fleetToRemove, status.Message))
				return makeResourceSyncCompletedDetails(commitHash, totalChanges, errorMessages), errorMessages, service.ApiStatusToErr(status)
			}
		}
	}
	api.SetStatusConditionByError(&rs.Status.Conditions, api.ConditionTypeResourceSyncSynced, "success", "fail", createUpdateErr)
	if createUpdateErr != nil {
		log.Errorf("Resource %s: failed to apply resource. error: %s", resourceName, createUpdateErr.Error())
		errorMessages = append(errorMessages, fmt.Sprintf("failed to apply resource: %s", createUpdateErr.Error()))
		return makeResourceSyncCompletedDetails(commitHash, totalChanges, errorMessages), errorMessages, createUpdateErr
	}
	rs.Status.ObservedGeneration = rs.Metadata.Generation
	log.Infof("Resource %s: %d fleets applied successfully\n", resourceName, len(fleets))
	return makeResourceSyncCompletedDetails(commitHash, totalChanges, errorMessages), errorMessages, createUpdateErr
}

func (r *ResourceSync) createOrUpdateMultiple(ctx context.Context, resources ...*api.Fleet) error {
	var errs []error
	for _, resource := range resources {
		_, status := r.serviceHandler.ReplaceFleet(ctx, *resource.Metadata.Name, *resource)
		if status.Code != http.StatusOK && status.Code != http.StatusCreated {
			if status.Message == flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error() {
				errs = append(errs, errors.New("one or more fleets are managed by a different resource"))
			} else {
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

func (r *ResourceSync) parseAndValidateResources(rs *api.ResourceSync, repo *api.Repository, gitCloneRepo cloneGitRepoFunc) ([]genericResourceMap, string, error) {
	path := rs.Spec.Path
	revision := rs.Spec.TargetRevision
	mfs, hash, err := gitCloneRepo(repo, &revision, lo.ToPtr(1))
	api.SetStatusConditionByError(&rs.Status.Conditions, api.ConditionTypeResourceSyncAccessible, "accessible", "failed to clone repository", err)
	if err != nil {
		return nil, hash, err
	}

	if !NeedsSyncToHash(rs, hash) {
		// nothing to update
		r.log.Infof("resourcesync/%s: No new commits or path. skipping", *rs.Metadata.Name)
		return nil, hash, nil
	}
	api.SetStatusConditionByError(&rs.Status.Conditions, api.ConditionTypeResourceSyncSynced, "success", "fail", fmt.Errorf("out of sync"))

	rs.Status.ObservedCommit = lo.ToPtr(hash)

	// Open files
	fileInfo, err := mfs.Stat(path)
	api.SetStatusConditionByError(&rs.Status.Conditions, api.ConditionTypeResourceSyncAccessible, "accessible", "path not found in repository", err)
	if err != nil {
		return nil, hash, err
	}
	var resources []genericResourceMap
	if fileInfo.IsDir() {
		resources, err = r.extractResourcesFromDir(mfs, path)
	} else {
		resources, err = r.extractResourcesFromFile(mfs, path)
	}
	api.SetStatusConditionByError(&rs.Status.Conditions, api.ConditionTypeResourceSyncResourceParsed, "success", "fail", err)
	if err != nil {
		return nil, hash, err

	}
	return resources, hash, nil
}

func (r *ResourceSync) extractResourcesFromDir(mfs billy.Filesystem, path string) ([]genericResourceMap, error) {
	genericResources := []genericResourceMap{}
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

func (r *ResourceSync) extractResourcesFromFile(mfs billy.Filesystem, path string) ([]genericResourceMap, error) {
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

func (r ResourceSync) parseFleets(resources []genericResourceMap, owner *string) ([]*api.Fleet, error) {
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
				return nil, fmt.Errorf("failed validating fleet: %w", errors.Join(errs...))
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

func (r *ResourceSync) updateResourceSyncStatus(ctx context.Context, rs *api.ResourceSync) {
	_, status := r.serviceHandler.ReplaceResourceSyncStatus(ctx, *rs.Metadata.Name, *rs)
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

func (r *ResourceSync) validateFleetNameConflicts(ctx context.Context, fleets []*api.Fleet, owner string) error {
	var conflictingFleets []string

	for _, fleet := range fleets {
		fleetName := *fleet.Metadata.Name
		// Check if a fleet with this name already exists
		existingFleet, status := r.serviceHandler.GetFleet(ctx, fleetName, api.GetFleetParams{})
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
