package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
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
	cfg                   *config.Config
	ignoreResourceUpdates []string
}

type GenericResourceMap map[string]interface{}

var validFileExtensions = []string{"json", "yaml", "yml"}
var supportedResources = []string{domain.FleetKind, domain.CatalogKind, domain.CatalogItemKind}

func NewResourceSync(serviceHandler service.Service, log logrus.FieldLogger, cfg *config.Config, ignoreResourceUpdates []string) *ResourceSync {
	return &ResourceSync{
		log:                   log,
		serviceHandler:        serviceHandler,
		cfg:                   cfg,
		ignoreResourceUpdates: ignoreResourceUpdates,
	}
}

func (r *ResourceSync) Poll(ctx context.Context, orgId uuid.UUID) {
	log := log.WithReqIDFromCtx(ctx, r.log)

	log.Info("Running ResourceSync Polling")

	limit := int32(ItemsPerPage)
	continueToken := (*string)(nil)

	for {
		resourcesyncs, status := r.serviceHandler.ListResourceSyncs(ctx, orgId, domain.ListResourceSyncsParams{
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

func (r *ResourceSync) run(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID, rs *domain.ResourceSync) error {
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

	syncType := domain.ResourceSyncTypeFleet
	if rs.Spec.Type != nil {
		syncType = *rs.Spec.Type
	}

	switch syncType {
	case domain.ResourceSyncTypeFleet:
		return r.syncFleetResources(ctx, log, orgId, rs, resources, resourceName)
	case domain.ResourceSyncTypeCatalog:
		return r.syncCatalogResources(ctx, log, orgId, rs, resources, resourceName)
	default:
		return fmt.Errorf("resource %s: unsupported sync type %q", resourceName, syncType)
	}
}

func (r *ResourceSync) syncFleetResources(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID, rs *domain.ResourceSync, resources []GenericResourceMap, resourceName string) error {
	fleetResources := filterByKind(resources, domain.FleetKind)

	if unexpected := unexpectedKinds(resources, domain.FleetKind); len(unexpected) > 0 {
		err := fmt.Errorf("resource %s: sync type is fleet but found unexpected kind(s): %v", resourceName, unexpected)
		domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncResourceParsed, "success", "fail", err)
		return err
	}

	fleets, err := r.parseFleets(fleetResources)
	if err != nil {
		parseErr := fmt.Errorf("resource %s: error: %w", resourceName, err)
		domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncResourceParsed, "success", "fail", parseErr)
		log.Error(parseErr)
		return parseErr
	}
	domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncResourceParsed, "success", "fail", nil)

	if err := r.SyncFleets(ctx, log, orgId, rs, fleets, resourceName); err != nil {
		return err
	}
	domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncSynced, "success", "fail", nil)
	rs.Status.ObservedGeneration = rs.Metadata.Generation
	return nil
}

func (r *ResourceSync) syncCatalogResources(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID, rs *domain.ResourceSync, resources []GenericResourceMap, resourceName string) error {
	catalogResources := filterByKind(resources, domain.CatalogKind)
	itemResources := filterByKind(resources, domain.CatalogItemKind)

	if unexpected := unexpectedKinds(resources, domain.CatalogKind, domain.CatalogItemKind); len(unexpected) > 0 {
		err := fmt.Errorf("resource %s: sync type is catalog but found unexpected kind(s): %v", resourceName, unexpected)
		domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncResourceParsed, "success", "fail", err)
		return err
	}

	catalogs, err := r.parseCatalogs(catalogResources)
	if err != nil {
		parseErr := fmt.Errorf("resource %s: error: %w", resourceName, err)
		domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncResourceParsed, "success", "fail", parseErr)
		log.Error(parseErr)
		return parseErr
	}

	items, err := r.parseCatalogItems(itemResources)
	if err != nil {
		parseErr := fmt.Errorf("resource %s: error: %w", resourceName, err)
		domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncResourceParsed, "success", "fail", parseErr)
		log.Error(parseErr)
		return parseErr
	}
	domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncResourceParsed, "success", "fail", nil)

	catalogsToRemove, err := r.SyncCatalogs(ctx, log, orgId, rs, catalogs, resourceName)
	if err != nil {
		return err
	}
	itemsToRemove, err := r.SyncCatalogItems(ctx, log, orgId, rs, items, resourceName)
	if err != nil {
		return err
	}

	if err := r.deleteStaleCatalogItems(ctx, log, orgId, rs, itemsToRemove, resourceName); err != nil {
		return err
	}
	if err := r.deleteStaleCatalogs(ctx, log, orgId, rs, catalogsToRemove, resourceName); err != nil {
		return err
	}

	domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncSynced, "success", "fail", nil)
	rs.Status.ObservedGeneration = rs.Metadata.Generation
	return nil
}

// unexpectedKinds returns any resource kinds not in the allowed set.
func unexpectedKinds(resources []GenericResourceMap, allowed ...string) []string {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		allowedSet[a] = struct{}{}
	}
	seen := make(map[string]struct{})
	for _, r := range resources {
		if k, ok := r["kind"].(string); ok {
			if _, isAllowed := allowedSet[k]; !isAllowed {
				seen[k] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(seen))
	for k := range seen {
		result = append(result, k)
	}
	return result
}

// GetRepositoryAndValidateAccess gets the repository and validates it's accessible
func (r *ResourceSync) GetRepositoryAndValidateAccess(ctx context.Context, orgId uuid.UUID, rs *domain.ResourceSync) (*domain.Repository, error) {
	if rs == nil {
		return nil, fmt.Errorf("ResourceSync is nil")
	}

	repoName := rs.Spec.Repository
	repo, status := r.serviceHandler.GetRepository(ctx, orgId, repoName)
	err := service.ApiStatusToErr(status)

	// Ensure Status and Conditions are initialized
	if rs.Status == nil {
		rs.Status = &domain.ResourceSyncStatus{}
	}
	if rs.Status.Conditions == nil {
		rs.Status.Conditions = []domain.Condition{}
	}

	domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncAccessible, "accessible", "repository resource not found", err)
	if err != nil {
		return nil, err
	}
	return repo, nil
}

// ParseFleetsFromResources parses fleets from generic resources
func (r *ResourceSync) ParseFleetsFromResources(resources []GenericResourceMap, resourceName string) ([]*domain.Fleet, error) {
	fleets, err := r.parseFleets(resources)
	// Note: We can't set conditions here since we don't have access to rs
	// The conditions will be set in the calling method
	if err != nil {
		return nil, fmt.Errorf("resource %s: error: %w", resourceName, err)
	}
	return fleets, nil
}

// SyncFleets syncs the fleets to the service
func (r *ResourceSync) SyncFleets(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID, rs *domain.ResourceSync, fleets []*domain.Fleet, resourceName string) error {
	if rs == nil {
		return fmt.Errorf("ResourceSync is nil")
	}

	// Ensure Status and Conditions are initialized
	if rs.Status == nil {
		rs.Status = &domain.ResourceSyncStatus{}
	}
	if rs.Status.Conditions == nil {
		rs.Status.Conditions = []domain.Condition{}
	}

	owner := util.SetResourceOwner(domain.ResourceSyncKind, resourceName)

	// Validate that no fleet names conflict with fleets owned by other ResourceSyncs
	if err := r.validateFleetNameConflicts(ctx, orgId, fleets, *owner); err != nil {
		validateErr := fmt.Errorf("resource %s: error: %w", resourceName, err)
		log.Error(validateErr)
		domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncSynced, "success", "fail", validateErr)
		return validateErr
	}

	fleetsPreOwned := make([]domain.Fleet, 0)

	listParams := domain.ListFleetsParams{
		Limit:         lo.ToPtr(int32(100)),
		FieldSelector: lo.ToPtr(fmt.Sprintf("metadata.owner=%s", *owner)),
	}
	for {
		var listRes *domain.FleetList
		var status domain.Status
		listRes, status = r.serviceHandler.ListFleets(ctx, orgId, listParams)
		if status.Code != http.StatusOK {
			listErr := fmt.Errorf("resource %s: failed to list owned fleets. error: %s", resourceName, status.Message)
			log.Error(listErr)
			domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncSynced, "success", "fail", listErr)
			return listErr
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
		// Set ResourceSyncRequestCtxKey to allow resource sync to delete resources it owns
		deleteCtx := context.WithValue(ctx, consts.ResourceSyncRequestCtxKey, true)
		for _, fleetToRemove := range fleetsToRemove {
			status := r.serviceHandler.DeleteFleet(deleteCtx, orgId, fleetToRemove)
			if status.Code != http.StatusOK {
				err := fmt.Errorf("resource %s: failed to remove old fleet %s. error: %s", resourceName, fleetToRemove, status.Message)
				log.Error(err)
				domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncSynced, "success", "fail", err)
				return service.ApiStatusToErr(status)
			}
		}
	}
	domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncSynced, "success", "fail", createUpdateErr)
	if createUpdateErr != nil {
		log.Errorf("Resource %s: failed to apply resource. error: %s", resourceName, createUpdateErr.Error())
		return createUpdateErr
	}
	log.Infof("Resource %s: %d fleets applied successfully\n", resourceName, len(fleets))
	return nil
}

func (r *ResourceSync) createOrUpdateMultiple(ctx context.Context, orgId uuid.UUID, owner *string, resources ...*domain.Fleet) error {
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
func fleetsDelta(owned []domain.Fleet, newOwned []*domain.Fleet) []string {
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
func NeedsSyncToHash(rs *domain.ResourceSync, hash string) bool {
	if rs.Status == nil || rs.Status.Conditions == nil {
		return true
	}

	if domain.IsStatusConditionFalse(rs.Status.Conditions, domain.ConditionTypeResourceSyncSynced) {
		return true
	}

	var observedGen int64 = 0
	if rs.Status.ObservedGeneration != nil {
		observedGen = *rs.Status.ObservedGeneration
	}
	var prevHash = util.DefaultIfNil(rs.Status.ObservedCommit, "")
	return hash != prevHash || observedGen != *rs.Metadata.Generation
}

func (r *ResourceSync) parseAndValidateResources(rs *domain.ResourceSync, repo *domain.Repository, gitCloneRepo cloneGitRepoFunc) ([]GenericResourceMap, error) {
	path := rs.Spec.Path
	revision := rs.Spec.TargetRevision
	mfs, hash, err := gitCloneRepo(repo, &revision, lo.ToPtr(1), r.cfg)
	domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncAccessible, "accessible", "failed to clone repository", err)
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
	domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncSynced, "success", domain.ResourceSyncNewHashDetectedReason, fmt.Errorf("detected new hash %s", hash))

	rs.Status.ObservedCommit = lo.ToPtr(hash)

	// Open files
	fileInfo, err := mfs.Stat(path)
	domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncAccessible, "accessible", "path not found in repository", err)
	if err != nil {
		return nil, err
	}
	var resources []GenericResourceMap
	if fileInfo.IsDir() {
		resources, err = r.extractResourcesFromDir(mfs, path)
	} else {
		resources, err = r.extractResourcesFromFile(mfs, path)
	}
	domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncResourceParsed, "success", "fail", err)
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
		fullPath := mfs.Join(path, file.Name())
		if file.IsDir() {
			subResources, err := r.extractResourcesFromDir(mfs, fullPath)
			if err != nil {
				return nil, err
			}
			genericResources = append(genericResources, subResources...)
		} else if isValidFile(file.Name()) {
			resources, err := r.extractResourcesFromFile(mfs, fullPath)
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
			// Skip unsupported kinds to allow mixed-content repos
			continue
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

func (r ResourceSync) parseFleets(resources []GenericResourceMap) ([]*domain.Fleet, error) {
	fleets := make([]*domain.Fleet, 0)
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
		case domain.FleetKind:
			var fleet domain.Fleet
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
			continue // skip non-Fleet resources
		}
	}

	return fleets, nil
}

func (r *ResourceSync) updateResourceSyncStatus(ctx context.Context, orgId uuid.UUID, rs *domain.ResourceSync) {
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

func (r *ResourceSync) validateFleetNameConflicts(ctx context.Context, orgId uuid.UUID, fleets []*domain.Fleet, owner string) error {
	var conflictingFleets []string

	for _, fleet := range fleets {
		fleetName := *fleet.Metadata.Name
		// Check if a fleet with this name already exists
		existingFleet, status := r.serviceHandler.GetFleet(ctx, orgId, fleetName, domain.GetFleetParams{})
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

// filterByKind returns only resources matching the given kind.
func filterByKind(resources []GenericResourceMap, kind string) []GenericResourceMap {
	filtered := make([]GenericResourceMap, 0)
	for _, r := range resources {
		if k, ok := r["kind"].(string); ok && k == kind {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

func (r *ResourceSync) parseCatalogs(resources []GenericResourceMap) ([]*domain.Catalog, error) {
	catalogs := make([]*domain.Catalog, 0)
	names := make(map[string]bool)
	for _, resource := range resources {
		kind, ok := resource["kind"].(string)
		if !ok || kind != domain.CatalogKind {
			continue
		}
		buf, err := json.Marshal(resource)
		if err != nil {
			return nil, fmt.Errorf("failed to parse generic resource: %w", err)
		}
		var catalog domain.Catalog
		if err := yamlutil.Unmarshal(buf, &catalog); err != nil {
			return nil, fmt.Errorf("decoding Catalog resource: %w", err)
		}
		if catalog.Metadata.Name == nil {
			return nil, fmt.Errorf("decoding Catalog resource: missing field .metadata.name")
		}
		if errs := catalog.Validate(); len(errs) > 0 {
			return nil, fmt.Errorf("failed validating catalog %s: %w", *catalog.Metadata.Name, errors.Join(errs...))
		}
		if names[*catalog.Metadata.Name] {
			return nil, fmt.Errorf("found multiple catalog definitions with name '%s'", *catalog.Metadata.Name)
		}
		names[*catalog.Metadata.Name] = true
		catalogs = append(catalogs, &catalog)
	}
	return catalogs, nil
}

func (r *ResourceSync) parseCatalogItems(resources []GenericResourceMap) ([]*domain.CatalogItem, error) {
	items := make([]*domain.CatalogItem, 0)
	keys := make(map[string]bool)
	for _, resource := range resources {
		kind, ok := resource["kind"].(string)
		if !ok || kind != domain.CatalogItemKind {
			continue
		}
		buf, err := json.Marshal(resource)
		if err != nil {
			return nil, fmt.Errorf("failed to parse generic resource: %w", err)
		}
		var item domain.CatalogItem
		if err := yamlutil.Unmarshal(buf, &item); err != nil {
			return nil, fmt.Errorf("decoding CatalogItem resource: %w", err)
		}
		if item.Metadata.Name == nil {
			return nil, fmt.Errorf("decoding CatalogItem resource: missing field .metadata.name")
		}
		if item.Metadata.Catalog == "" {
			return nil, fmt.Errorf("decoding CatalogItem %s: missing field .metadata.catalog", *item.Metadata.Name)
		}
		if errs := item.Validate(); len(errs) > 0 {
			return nil, fmt.Errorf("failed validating catalog item %s/%s: %w", item.Metadata.Catalog, *item.Metadata.Name, errors.Join(errs...))
		}
		key := fmt.Sprintf("%s/%s", item.Metadata.Catalog, *item.Metadata.Name)
		if keys[key] {
			return nil, fmt.Errorf("found multiple catalog item definitions with key '%s'", key)
		}
		keys[key] = true
		items = append(items, &item)
	}
	return items, nil
}

// SyncCatalogs creates/updates catalogs and returns the list of stale catalog names to delete.
// Deletion is handled separately by the caller to ensure correct dependency ordering
// (CatalogItems must be deleted before Catalogs).
func (r *ResourceSync) SyncCatalogs(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID, rs *domain.ResourceSync, catalogs []*domain.Catalog, resourceName string) ([]string, error) {
	if rs == nil {
		return nil, fmt.Errorf("ResourceSync is nil")
	}
	if rs.Status == nil {
		rs.Status = &domain.ResourceSyncStatus{}
	}
	if rs.Status.Conditions == nil {
		rs.Status.Conditions = []domain.Condition{}
	}

	owner := util.SetResourceOwner(domain.ResourceSyncKind, resourceName)

	// Validate name conflicts
	if err := r.validateCatalogNameConflicts(ctx, orgId, catalogs, *owner); err != nil {
		validateErr := fmt.Errorf("resource %s: error: %w", resourceName, err)
		log.Error(validateErr)
		domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncSynced, "success", "fail", validateErr)
		return nil, validateErr
	}

	// List pre-owned catalogs
	catalogsPreOwned := make([]domain.Catalog, 0)
	listParams := domain.ListCatalogsParams{
		Limit:         lo.ToPtr(int32(100)),
		FieldSelector: lo.ToPtr(fmt.Sprintf("metadata.owner=%s", *owner)),
	}
	for {
		listRes, status := r.serviceHandler.ListCatalogs(ctx, orgId, listParams)
		if status.Code != http.StatusOK {
			err := fmt.Errorf("resource %s: failed to list owned catalogs: %s", resourceName, status.Message)
			log.Error(err)
			domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncSynced, "success", "fail", err)
			return nil, err
		}
		catalogsPreOwned = append(catalogsPreOwned, listRes.Items...)
		if listRes.Metadata.Continue == nil {
			break
		}
		listParams.Continue = listRes.Metadata.Continue
	}

	toRemove := catalogsDelta(catalogsPreOwned, catalogs)

	if len(catalogs) > 0 {
		log.Infof("Resource %s: applying %d catalogs", resourceName, len(catalogs))
		createUpdateErr := r.createOrUpdateCatalogs(ctx, orgId, owner, catalogs...)
		if errors.Is(createUpdateErr, flterrors.ErrUpdatingResourceWithOwnerNotAllowed) {
			log.Errorf("one or more catalogs are managed by a different resource. %v", createUpdateErr)
		}

		if createUpdateErr != nil {
			domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncSynced, "success", "fail", createUpdateErr)
			log.Errorf("Resource %s: failed to apply catalogs: %s", resourceName, createUpdateErr.Error())
			return nil, createUpdateErr
		}
		log.Infof("Resource %s: %d catalogs applied successfully", resourceName, len(catalogs))
	}
	return toRemove, nil
}

func (r *ResourceSync) createOrUpdateCatalogs(ctx context.Context, orgId uuid.UUID, owner *string, resources ...*domain.Catalog) error {
	var errs []error
	for _, resource := range resources {
		externalCtx := context.WithValue(ctx, consts.InternalRequestCtxKey, false)
		externalCtx = context.WithValue(externalCtx, consts.ResourceSyncRequestCtxKey, true)
		updatedCatalog, status := r.serviceHandler.ReplaceCatalog(externalCtx, orgId, *resource.Metadata.Name, *resource)
		if status.Code != http.StatusOK && status.Code != http.StatusCreated {
			if status.Message == flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error() {
				errs = append(errs, errors.New("one or more catalogs are managed by a different resource"))
			} else {
				errs = append(errs, service.ApiStatusToErr(status))
			}
		}

		// Set owner if not already set
		if updatedCatalog != nil && util.DefaultIfNil(updatedCatalog.Metadata.Owner, "") != util.DefaultIfNil(owner, "") {
			updatedCatalog.Metadata.Owner = owner
			_, status := r.serviceHandler.ReplaceCatalog(ctx, orgId, *resource.Metadata.Name, *updatedCatalog)
			if status.Code != http.StatusOK {
				errs = append(errs, service.ApiStatusToErr(status))
			}
		}
	}
	return errors.Join(lo.Uniq(errs)...)
}

func catalogsDelta(owned []domain.Catalog, newOwned []*domain.Catalog) []string {
	toRemove := make([]string, 0)
	for _, ownedCatalog := range owned {
		found := false
		name := ownedCatalog.Metadata.Name
		for _, newCatalog := range newOwned {
			if *name == *newCatalog.Metadata.Name {
				found = true
				break
			}
		}
		if !found {
			toRemove = append(toRemove, *name)
		}
	}
	return toRemove
}

func (r *ResourceSync) validateCatalogNameConflicts(ctx context.Context, orgId uuid.UUID, catalogs []*domain.Catalog, owner string) error {
	var conflicts []string
	for _, catalog := range catalogs {
		name := *catalog.Metadata.Name
		existing, status := r.serviceHandler.GetCatalog(ctx, orgId, name)
		if status.Code == http.StatusOK {
			if existing.Metadata.Owner != nil && *existing.Metadata.Owner != owner {
				conflicts = append(conflicts, name)
			}
		} else if status.Code != http.StatusNotFound {
			return fmt.Errorf("failed to check existing catalog '%s': %s", name, status.Message)
		}
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("catalog name(s) %v conflict with existing catalogs managed by different ResourceSyncs", conflicts)
	}
	return nil
}

// SyncCatalogItems creates/updates catalog items and returns the list of stale item keys to delete.
// Deletion is handled separately by the caller to ensure correct dependency ordering
// (CatalogItems must be deleted before Catalogs).
func (r *ResourceSync) SyncCatalogItems(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID, rs *domain.ResourceSync, items []*domain.CatalogItem, resourceName string) ([]string, error) {
	if rs == nil {
		return nil, fmt.Errorf("ResourceSync is nil")
	}
	if rs.Status == nil {
		rs.Status = &domain.ResourceSyncStatus{}
	}
	if rs.Status.Conditions == nil {
		rs.Status.Conditions = []domain.Condition{}
	}

	owner := util.SetResourceOwner(domain.ResourceSyncKind, resourceName)

	// Validate item conflicts (includes cross-RS parent guard)
	if err := r.validateCatalogItemConflicts(ctx, orgId, items, *owner); err != nil {
		validateErr := fmt.Errorf("resource %s: error: %w", resourceName, err)
		log.Error(validateErr)
		domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncSynced, "success", "fail", validateErr)
		return nil, validateErr
	}

	// List pre-owned items
	itemsPreOwned := make([]domain.CatalogItem, 0)
	listParams := domain.ListAllCatalogItemsParams{
		Limit:         lo.ToPtr(int32(100)),
		FieldSelector: lo.ToPtr(fmt.Sprintf("metadata.owner=%s", *owner)),
	}
	for {
		listRes, status := r.serviceHandler.ListAllCatalogItems(ctx, orgId, listParams)
		if status.Code != http.StatusOK {
			err := fmt.Errorf("resource %s: failed to list owned catalog items: %s", resourceName, status.Message)
			log.Error(err)
			domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncSynced, "success", "fail", err)
			return nil, err
		}
		itemsPreOwned = append(itemsPreOwned, listRes.Items...)
		if listRes.Metadata.Continue == nil {
			break
		}
		listParams.Continue = listRes.Metadata.Continue
	}

	toRemove := catalogItemsDelta(itemsPreOwned, items)

	if len(items) > 0 {
		log.Infof("Resource %s: applying %d catalog items", resourceName, len(items))
		createUpdateErr := r.createOrUpdateCatalogItems(ctx, orgId, owner, items...)
		if errors.Is(createUpdateErr, flterrors.ErrUpdatingResourceWithOwnerNotAllowed) {
			log.Errorf("one or more catalog items are managed by a different resource. %v", createUpdateErr)
		}

		if createUpdateErr != nil {
			domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncSynced, "success", "fail", createUpdateErr)
			log.Errorf("Resource %s: failed to apply catalog items: %s", resourceName, createUpdateErr.Error())
			return nil, createUpdateErr
		}
		log.Infof("Resource %s: %d catalog items applied successfully", resourceName, len(items))
	}
	return toRemove, nil
}

func (r *ResourceSync) createOrUpdateCatalogItems(ctx context.Context, orgId uuid.UUID, owner *string, items ...*domain.CatalogItem) error {
	var errs []error
	for _, item := range items {
		externalCtx := context.WithValue(ctx, consts.InternalRequestCtxKey, false)
		externalCtx = context.WithValue(externalCtx, consts.ResourceSyncRequestCtxKey, true)
		updatedItem, status := r.serviceHandler.ReplaceCatalogItem(externalCtx, orgId, item.Metadata.Catalog, *item.Metadata.Name, *item)
		if status.Code != http.StatusOK && status.Code != http.StatusCreated {
			if status.Message == flterrors.ErrUpdatingResourceWithOwnerNotAllowed.Error() {
				errs = append(errs, errors.New("one or more catalog items are managed by a different resource"))
			} else {
				errs = append(errs, service.ApiStatusToErr(status))
			}
			continue
		}

		// Set owner if not already set
		if updatedItem != nil && util.DefaultIfNil(updatedItem.Metadata.Owner, "") != util.DefaultIfNil(owner, "") {
			updatedItem.Metadata.Owner = owner
			_, status := r.serviceHandler.ReplaceCatalogItem(ctx, orgId, updatedItem.Metadata.Catalog, *updatedItem.Metadata.Name, *updatedItem)
			if status.Code != http.StatusOK {
				errs = append(errs, service.ApiStatusToErr(status))
			}
		}
	}
	return errors.Join(lo.Uniq(errs)...)
}

// deleteStaleCatalogs deletes catalogs that are no longer present in git.
func (r *ResourceSync) deleteStaleCatalogs(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID, rs *domain.ResourceSync, toRemove []string, resourceName string) error {
	if len(toRemove) == 0 {
		return nil
	}
	log.Infof("Resource %s: found #%d catalogs to remove. removing", resourceName, len(toRemove))
	deleteCtx := context.WithValue(ctx, consts.ResourceSyncRequestCtxKey, true)
	for _, catalogName := range toRemove {
		status := r.serviceHandler.DeleteCatalog(deleteCtx, orgId, catalogName)
		if status.Code != http.StatusOK {
			err := fmt.Errorf("resource %s: failed to remove old catalog %s: %s", resourceName, catalogName, status.Message)
			log.Error(err)
			domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncSynced, "success", "fail", err)
			return service.ApiStatusToErr(status)
		}
	}
	return nil
}

// deleteStaleCatalogItems deletes catalog items that are no longer present in git.
func (r *ResourceSync) deleteStaleCatalogItems(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID, rs *domain.ResourceSync, toRemove []string, resourceName string) error {
	if len(toRemove) == 0 {
		return nil
	}
	log.Infof("Resource %s: found #%d catalog items to remove. removing", resourceName, len(toRemove))
	deleteCtx := context.WithValue(ctx, consts.ResourceSyncRequestCtxKey, true)
	for _, key := range toRemove {
		parts := strings.SplitN(key, "/", 2)
		if len(parts) != 2 {
			continue
		}
		status := r.serviceHandler.DeleteCatalogItem(deleteCtx, orgId, parts[0], parts[1])
		if status.Code != http.StatusOK {
			err := fmt.Errorf("resource %s: failed to remove old catalog item %s: %s", resourceName, key, status.Message)
			log.Error(err)
			domain.SetStatusConditionByError(&rs.Status.Conditions, domain.ConditionTypeResourceSyncSynced, "success", "fail", err)
			return service.ApiStatusToErr(status)
		}
	}
	return nil
}

// catalogItemsDelta returns catalog/name keys of owned items not present in the new set.
func catalogItemsDelta(owned []domain.CatalogItem, newOwned []*domain.CatalogItem) []string {
	toRemove := make([]string, 0)
	for _, ownedItem := range owned {
		found := false
		key := fmt.Sprintf("%s/%s", ownedItem.Metadata.Catalog, lo.FromPtr(ownedItem.Metadata.Name))
		for _, newItem := range newOwned {
			newKey := fmt.Sprintf("%s/%s", newItem.Metadata.Catalog, lo.FromPtr(newItem.Metadata.Name))
			if key == newKey {
				found = true
				break
			}
		}
		if !found {
			toRemove = append(toRemove, key)
		}
	}
	return toRemove
}

func (r *ResourceSync) validateCatalogItemConflicts(ctx context.Context, orgId uuid.UUID, items []*domain.CatalogItem, owner string) error {
	var conflicts []string
	var parentConflicts []string

	catalogOwners := make(map[string]*string)
	catalogOwnerChecked := make(map[string]bool)

	for _, item := range items {
		// Check item-level ownership conflict
		existing, status := r.serviceHandler.GetCatalogItem(ctx, orgId, item.Metadata.Catalog, *item.Metadata.Name)
		if status.Code == http.StatusOK {
			if existing.Metadata.Owner != nil && *existing.Metadata.Owner != owner {
				conflicts = append(conflicts, fmt.Sprintf("%s/%s", item.Metadata.Catalog, *item.Metadata.Name))
			}
		} else if status.Code != http.StatusNotFound {
			return fmt.Errorf("failed to check existing catalog item '%s/%s': %s", item.Metadata.Catalog, *item.Metadata.Name, status.Message)
		}

		// Check parent catalog ownership, items must not reference a catalog owned by a different RS
		catalogName := item.Metadata.Catalog
		if !catalogOwnerChecked[catalogName] {
			catalogOwnerChecked[catalogName] = true
			catalog, catStatus := r.serviceHandler.GetCatalog(ctx, orgId, catalogName)
			if catStatus.Code == http.StatusOK {
				catalogOwners[catalogName] = catalog.Metadata.Owner
			} else if catStatus.Code != http.StatusNotFound {
				return fmt.Errorf("failed to check existing catalog '%s': %s", catalogName, catStatus.Message)
			}
		}
		if parentOwner, ok := catalogOwners[catalogName]; ok && parentOwner != nil && *parentOwner != owner {
			parentConflicts = append(parentConflicts, fmt.Sprintf("%s/%s (Catalog %s owned by %s)",
				item.Metadata.Catalog, *item.Metadata.Name, catalogName, *parentOwner))
		}
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("catalog item(s) %v conflict with existing items managed by different ResourceSyncs", conflicts)
	}
	if len(parentConflicts) > 0 {
		return fmt.Errorf("catalog item(s) reference catalogs owned by different ResourceSyncs: %v", parentConflicts)
	}
	return nil
}
