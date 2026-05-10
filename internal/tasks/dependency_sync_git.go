package tasks

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

type gitLsRemoteFunc func(ctx context.Context, repoURL string, refs []string,
	auth transport.AuthMethod) (map[string]string, error)

type DependencySyncGit struct {
	log            logrus.FieldLogger
	serviceHandler service.Service
	cfg            *config.Config
	lsRemote       gitLsRemoteFunc
	maxConcurrent  int
}

func NewDependencySyncGit(log logrus.FieldLogger, serviceHandler service.Service,
	cfg *config.Config) *DependencySyncGit {
	return &DependencySyncGit{
		log:            log,
		serviceHandler: serviceHandler,
		cfg:            cfg,
		lsRemote:       GitLsRemote,
		maxConcurrent:  10,
	}
}

// probeResult holds the outcome of a single (repo, revision) probe.
type probeResult struct {
	probe       *model.GitDependencyProbe
	resourceKey string
	newSHA      string
	changed     bool
	firstSeen   bool
}

func (d *DependencySyncGit) Poll(ctx context.Context, orgId uuid.UUID) {
	pollInterval := d.cfg.GetDependenciesSyncPollInterval()

	probes, status := d.serviceHandler.ListDueGitDependencies(ctx, orgId, pollInterval)
	if status.Code != http.StatusOK {
		d.log.Errorf("failed listing due git dependencies: %s", status.Message)
		return
	}
	if len(probes) == 0 {
		return
	}

	// Group probes by repository so we fetch each repo's URL and auth once,
	// then call ls-remote with all revisions for that repo in one call.
	repoGroups := make(map[string][]*model.GitDependencyProbe)
	for i := range probes {
		repoGroups[probes[i].RepositoryName] = append(repoGroups[probes[i].RepositoryName], &probes[i])
	}

	var (
		mu      sync.Mutex
		results []probeResult
	)

	sem := make(chan struct{}, d.maxConcurrent)
	var wg sync.WaitGroup

	for repoName, group := range repoGroups {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res := d.probeRepo(ctx, repoName, group)
			mu.Lock()
			results = append(results, res...)
			mu.Unlock()
		}()
	}

	wg.Wait()

	d.reconcile(ctx, orgId, results)
}

// probeRepo uses the repository spec carried by the probes (from the SQL JOIN)
// to extract the URL and auth, calls ls-remote for all revisions in the group,
// and returns a probeResult per revision.
func (d *DependencySyncGit) probeRepo(ctx context.Context,
	repoName string, group []*model.GitDependencyProbe) []probeResult {

	if group[0].RepoSpec == nil {
		d.log.Warnf("repository %s not found (no spec in JOIN result)", repoName)
		return nil
	}

	spec := group[0].RepoSpec.Data
	repoURL, err := spec.GetRepoURL()
	if err != nil {
		d.log.WithError(err).Warnf("failed getting URL for repository %s", repoName)
		return nil
	}

	repo := &domain.Repository{Spec: spec}
	auth, err := GetAuth(repo, d.cfg)
	if err != nil {
		d.log.WithError(err).Warnf("failed getting auth for repository %s", repoName)
		return nil
	}

	revisions := make([]string, len(group))
	for i, p := range group {
		revisions[i] = p.Revision
	}

	resolved, err := d.lsRemote(ctx, repoURL, revisions, auth)
	if err != nil {
		d.log.WithError(err).Warnf("git ls-remote failed for %s", repoName)
		return nil
	}

	var results []probeResult
	for _, p := range group {
		newSHA, found := resolved[p.Revision]
		if !found {
			d.log.Warnf("ref %s not found in repository %s", p.Revision, repoName)
			continue
		}

		rk := fmt.Sprintf("git:%s/%s", repoName, p.Revision)
		r := probeResult{
			probe:       p,
			resourceKey: rk,
			newSHA:      newSHA,
		}

		if p.Fingerprint == nil {
			r.firstSeen = true
		} else if newSHA != *p.Fingerprint {
			r.changed = true
		}

		results = append(results, r)
	}
	return results
}

// reconcile batch-updates sync states and emits events for changed probes.
func (d *DependencySyncGit) reconcile(ctx context.Context, orgId uuid.UUID, results []probeResult) {
	now := time.Now().UTC()

	var upsertStates []model.SyncState
	var unchangedKeys []string

	for _, r := range results {
		if r.firstSeen || r.changed {
			upsertStates = append(upsertStates, model.SyncState{
				OrgID:         orgId,
				ResourceKey:   r.resourceKey,
				Fingerprint:   r.newSHA,
				LastCheckedAt: now,
				LastChangeAt:  &now,
			})
		} else {
			unchangedKeys = append(unchangedKeys, r.resourceKey)
		}
	}

	if len(upsertStates) > 0 {
		if st := d.serviceHandler.BulkUpsertSyncState(ctx, orgId, upsertStates); st.Code != http.StatusOK {
			d.log.Errorf("failed bulk upserting sync states: %s", st.Message)
			return
		}
	}

	if len(unchangedKeys) > 0 {
		if st := d.serviceHandler.BulkUpdateSyncStateLastCheckedAt(ctx, orgId, unchangedKeys, now); st.Code != http.StatusOK {
			d.log.Errorf("failed bulk updating last_checked_at: %s", st.Message)
		}
	}

	for _, r := range results {
		if !r.changed {
			continue
		}
		for _, fleetName := range r.probe.FleetNames {
			event := common.GetDependencyChangeDetectedEvent(ctx, domain.FleetKind, fleetName, r.resourceKey, r.newSHA)
			if event != nil {
				d.serviceHandler.CreateEvent(ctx, orgId, event)
			}
		}
		for _, deviceName := range r.probe.DeviceNames {
			event := common.GetDependencyChangeDetectedEvent(ctx, domain.DeviceKind, deviceName, r.resourceKey, r.newSHA)
			if event != nil {
				d.serviceHandler.CreateEvent(ctx, orgId, event)
			}
		}
	}
}
