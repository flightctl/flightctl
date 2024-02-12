package resourcesync

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

type API interface {
	Test()
}

type ResourceSync struct {
	log   logrus.FieldLogger
	db    *gorm.DB
	store store.Store
}

type genericResourceMap map[string]interface{}

var fileExtensions = []string{"json", "yaml", "yml"}
var supportedResources = []string{model.FleetKind}

func NewResourceSync(log logrus.FieldLogger, store store.Store) *ResourceSync {
	return &ResourceSync{
		log:   log,
		store: store,
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
		defer r.updateResourceSyncStatus(rs)
		reponame := rs.Spec.Data.Repository
		repo, err := r.store.Repository().GetInternal(ctx, rs.OrgID, *reponame)
		if err != nil {
			// Failed to fetch Repository resource
			addRepoNotFoundCondition(rs, err)
			break
		}
		addRepoNotFoundCondition(rs, nil)
		resources, err := r.parseAndValidateResources(ctx, rs, repo)
		if err != nil {
			log.Errorf("resourcesync/%s: parsing failed. error: %s", rs.Name, err.Error())
			continue
		}

		fleets, err := r.parseFleets(resources, rs.OrgID)
		if err != nil {
			log.Errorf("resourcesync/%s: error: %s", rs.Name, err.Error())
			continue
		}

		r.log.Infof("resourcesync/%s: applying #%d fleets ", rs.Name, len(fleets))
		err = r.store.Fleet().CreateOrUpdateMultiple(ctx, rs.OrgID, fleets...)
		addSyncedCondition(rs, err)
		if err != nil {
			log.Errorf("resourcesync/%s: Failed to apply resource. error: %s", rs.Name, err.Error())
			break
		}
		r.log.Infof("resourcesync/%s #%d fleets applied successfully\n", rs.Name, len(fleets))
	}
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

	rs.Status.Data.LastSyncedCommitHash = &hash
	rs.Status.Data.LastSyncedPath = &path

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
		addResourceParseCondition(rs, err)
		return nil, err

	}
	addResourceParseCondition(rs, nil)
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

func (r ResourceSync) parseFleets(resources []genericResourceMap, orgId uuid.UUID) ([]*api.Fleet, error) {
	fleets := make([]*api.Fleet, 0)
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
	var prevHash *string = nil
	if rs.Status == nil {
		return true
	}
	_, syncedCondition := extractPrevConditionByType(&rs, syncedConditionType)
	if syncedCondition == nil || syncedCondition.Status != api.True {
		return true
	}
	var lastSyncedPath = rs.Status.Data.LastSyncedPath
	if rs.Status.Data.LastSyncedCommitHash != nil {
		prevHash = rs.Status.Data.LastSyncedCommitHash
	}
	return prevHash == nil || hash != *prevHash ||
		lastSyncedPath == nil || *lastSyncedPath != *rs.Spec.Data.Path
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
