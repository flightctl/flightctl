package tasks

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/google/uuid"
	"github.com/samber/lo"
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

// probeTarget groups refs by (repo, revision) to avoid redundant ls-remote calls.
type probeTarget struct {
	repoName string
	revision string
	// fleets/devices that will receive events if this target changes
	fleetNames  []string
	deviceNames []string
}

func (d *DependencySyncGit) Poll(ctx context.Context, orgId uuid.UUID) {
	refs, status := d.serviceHandler.ListDependencyRefsByRefType(ctx, orgId, "git")
	if status.Code != http.StatusOK {
		d.log.Errorf("failed listing git dependency refs: %s", status.Message)
		return
	}
	if len(refs) == 0 {
		return
	}

	targets := d.buildProbeTargets(refs)
	if len(targets) == 0 {
		return
	}

	pollInterval := d.cfg.GetDependenciesSyncPollInterval()

	sem := make(chan struct{}, d.maxConcurrent)
	var wg sync.WaitGroup

	for i := range targets {
		target := &targets[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			d.probeAndReconcile(ctx, orgId, target, pollInterval)
		}()
	}

	wg.Wait()
}

// buildProbeTargets groups refs by (repo, revision), skipping parameterized revisions.
func (d *DependencySyncGit) buildProbeTargets(refs []model.DependencyRef) []probeTarget {
	type targetKey struct{ repo, rev string }
	targetMap := make(map[targetKey]*probeTarget)

	for _, ref := range refs {
		fleetName := lo.FromPtr(ref.FleetName)
		deviceName := lo.FromPtr(ref.DeviceName)
		repoName := lo.FromPtr(ref.RepositoryName)
		revision := lo.FromPtr(ref.Revision)

		if containsTemplateParam(revision) {
			continue
		}

		key := targetKey{repo: repoName, rev: revision}
		t, ok := targetMap[key]
		if !ok {
			t = &probeTarget{repoName: repoName, revision: revision}
			targetMap[key] = t
		}

		if fleetName != "" {
			t.fleetNames = append(t.fleetNames, fleetName)
		}
		if deviceName != "" {
			t.deviceNames = append(t.deviceNames, deviceName)
		}
	}

	result := make([]probeTarget, 0, len(targetMap))
	for _, t := range targetMap {
		result = append(result, *t)
	}
	return result
}

func (d *DependencySyncGit) probeAndReconcile(ctx context.Context, orgId uuid.UUID, target *probeTarget, pollInterval time.Duration) {
	resourceKey := fmt.Sprintf("git:%s/%s", target.repoName, target.revision)

	existing, status := d.serviceHandler.GetSyncState(ctx, orgId, resourceKey)
	if status.Code != http.StatusOK {
		d.log.Errorf("failed reading sync state for %s: %s", resourceKey, status.Message)
		return
	}

	if existing != nil && time.Since(existing.LastCheckedAt) < pollInterval {
		return
	}

	repo, status := d.serviceHandler.GetRepository(ctx, orgId, target.repoName)
	if status.Code != http.StatusOK {
		d.log.Warnf("failed fetching repository %s: %s", target.repoName, status.Message)
		return
	}

	repoURL, err := repo.Spec.GetRepoURL()
	if err != nil {
		d.log.WithError(err).Warnf("failed getting URL for repository %s", target.repoName)
		return
	}

	auth, err := GetAuth(repo, d.cfg)
	if err != nil {
		d.log.WithError(err).Warnf("failed getting auth for repository %s", target.repoName)
		return
	}

	resolved, err := d.lsRemote(ctx, repoURL, []string{target.revision}, auth)
	if err != nil {
		d.log.WithError(err).Warnf("git ls-remote failed for %s ref %s", target.repoName, target.revision)
		return
	}

	newSHA, found := resolved[target.revision]
	if !found {
		d.log.Warnf("ref %s not found in repository %s", target.revision, target.repoName)
		return
	}

	now := time.Now().UTC()

	if existing == nil {
		if st := d.serviceHandler.SetSyncState(ctx, orgId, &model.SyncState{
			OrgID: orgId, ResourceKey: resourceKey,
			Fingerprint: newSHA, LastCheckedAt: now, LastChangeAt: &now,
		}); st.Code != http.StatusOK {
			d.log.Errorf("failed creating sync state for %s: %s", resourceKey, st.Message)
		}
		return
	}

	if newSHA == existing.Fingerprint {
		if st := d.serviceHandler.SetSyncStateLastCheckedAt(ctx, orgId, resourceKey, now); st.Code != http.StatusOK {
			d.log.Errorf("failed updating last_checked_at for %s: %s", resourceKey, st.Message)
		}
		return
	}

	if st := d.serviceHandler.SetSyncState(ctx, orgId, &model.SyncState{
		OrgID: orgId, ResourceKey: resourceKey,
		Fingerprint: newSHA, LastCheckedAt: now, LastChangeAt: &now,
	}); st.Code != http.StatusOK {
		d.log.Errorf("failed updating sync state for %s: %s", resourceKey, st.Message)
		return
	}

	for _, fleetName := range target.fleetNames {
		event := common.GetDependencyChangeDetectedEvent(ctx, domain.FleetKind, fleetName, resourceKey, newSHA)
		if event != nil {
			d.serviceHandler.CreateEvent(ctx, orgId, event)
		}
	}
	for _, deviceName := range target.deviceNames {
		event := common.GetDependencyChangeDetectedEvent(ctx, domain.DeviceKind, deviceName, resourceKey, newSHA)
		if event != nil {
			d.serviceHandler.CreateEvent(ctx, orgId, event)
		}
	}
}

func containsTemplateParam(s string) bool {
	return strings.Contains(s, "{{") && strings.Contains(s, "}}")
}
