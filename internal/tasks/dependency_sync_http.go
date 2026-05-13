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
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// httpConditionalGetFunc is the injectable function type for testing.
// Returns: fingerprint (ETag or Last-Modified value), HTTP status code, error.
// Empty fingerprint with status 200 means the endpoint doesn't support
// conditional requests (no ETag or Last-Modified in response).
type httpConditionalGetFunc func(ctx context.Context, repoURL string,
	httpSpec domain.HttpRepoSpec, storedFingerprint string) (fingerprint string, statusCode int, err error)

type DependencySyncHttp struct {
	log            logrus.FieldLogger
	serviceHandler service.Service
	cfg            *config.Config
	conditionalGet httpConditionalGetFunc
	maxConcurrent  int
}

func NewDependencySyncHttp(log logrus.FieldLogger, serviceHandler service.Service,
	cfg *config.Config) *DependencySyncHttp {
	return &DependencySyncHttp{
		log:            log,
		serviceHandler: serviceHandler,
		cfg:            cfg,
		conditionalGet: httpConditionalGet,
		maxConcurrent:  10,
	}
}

type httpProbeResult struct {
	probe       *model.HttpDependencyProbe
	resourceKey string
	fingerprint string
	changed     bool
	firstSeen   bool
	skip        bool // true for HttpNotConditional or errors
}

func (d *DependencySyncHttp) Poll(ctx context.Context, orgId uuid.UUID) {
	pollInterval := d.cfg.GetDependenciesSyncPollInterval()

	probes, status := d.serviceHandler.ListDueHttpDependencies(ctx, orgId, pollInterval)
	if status.Code != http.StatusOK {
		d.log.Errorf("failed listing due HTTP dependencies: %s", status.Message)
		return
	}
	if len(probes) == 0 {
		return
	}

	var (
		mu      sync.Mutex
		results []httpProbeResult
	)

	sem := make(chan struct{}, d.maxConcurrent)
	var wg sync.WaitGroup

	for i := range probes {
		probe := &probes[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r := d.probeEndpoint(ctx, probe)
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}()
	}

	wg.Wait()

	d.reconcile(ctx, orgId, results)
}

func (d *DependencySyncHttp) probeEndpoint(ctx context.Context, probe *model.HttpDependencyProbe) httpProbeResult {
	if probe.RepoSpec == nil {
		d.log.Warnf("repository %s not found (no spec in JOIN result)", probe.RepositoryName)
		return httpProbeResult{probe: probe, skip: true}
	}

	spec := probe.RepoSpec.Data
	httpSpec, err := spec.AsHttpRepoSpec()
	if err != nil {
		d.log.WithError(err).Warnf("failed decoding HTTP spec for repository %s", probe.RepositoryName)
		return httpProbeResult{probe: probe, skip: true}
	}

	repoURL := httpSpec.Url + probe.HTTPSuffix
	rk := fmt.Sprintf("http:%s/%s", probe.RepositoryName, probe.HTTPSuffix)

	storedFP := ""
	if probe.Fingerprint != nil {
		storedFP = *probe.Fingerprint
	}

	fingerprint, statusCode, err := d.conditionalGet(ctx, repoURL, httpSpec, storedFP)
	if err != nil {
		d.log.WithError(err).Warnf("HTTP probe failed for %s (status %d)", repoURL, statusCode)
		return httpProbeResult{probe: probe, skip: true}
	}

	r := httpProbeResult{
		probe:       probe,
		resourceKey: rk,
		fingerprint: fingerprint,
	}

	switch {
	case statusCode == http.StatusNotModified:
		// No change
	case statusCode == http.StatusOK && fingerprint == "":
		d.log.Warnf("endpoint %s does not support conditional requests (no ETag or Last-Modified)", repoURL)
		r.skip = true
	case probe.Fingerprint == nil:
		r.firstSeen = true
	case fingerprint != *probe.Fingerprint:
		r.changed = true
	}

	return r
}

func (d *DependencySyncHttp) reconcile(ctx context.Context, orgId uuid.UUID, results []httpProbeResult) {
	now := time.Now().UTC()

	for _, r := range results {
		if !r.changed {
			continue
		}
		for _, fleetName := range r.probe.FleetNames {
			event := common.GetDependencyChangeDetectedEvent(ctx, domain.FleetKind, fleetName, r.resourceKey, r.fingerprint)
			if event != nil {
				d.serviceHandler.CreateEvent(ctx, orgId, event)
			}
		}
		for _, deviceName := range r.probe.DeviceNames {
			event := common.GetDependencyChangeDetectedEvent(ctx, domain.DeviceKind, deviceName, r.resourceKey, r.fingerprint)
			if event != nil {
				d.serviceHandler.CreateEvent(ctx, orgId, event)
			}
		}
	}

	var upsertStates []model.SyncState
	var unchangedKeys []string

	for _, r := range results {
		if r.skip {
			continue
		}
		if r.firstSeen || r.changed {
			upsertStates = append(upsertStates, model.SyncState{
				OrgID:         orgId,
				ResourceKey:   r.resourceKey,
				Fingerprint:   r.fingerprint,
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
}

// httpConditionalGet sends a conditional GET to the given URL using stored
// fingerprint for If-None-Match/If-Modified-Since headers.
func httpConditionalGet(_ context.Context, repoURL string,
	httpSpec domain.HttpRepoSpec, storedFingerprint string) (string, int, error) {
	req, err := http.NewRequest("GET", repoURL, nil)
	if err != nil {
		return "", 0, fmt.Errorf("creating request: %w", err)
	}

	req, tlsConfig, err := buildHttpRepoRequestAuth(httpSpec, req)
	if err != nil {
		return "", 0, fmt.Errorf("building request auth: %w", err)
	}

	if storedFingerprint != "" {
		req.Header.Set("If-None-Match", storedFingerprint)
		req.Header.Set("If-Modified-Since", storedFingerprint)
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return "", http.StatusNotModified, nil
	case http.StatusOK:
		fingerprint := resp.Header.Get("ETag")
		if fingerprint == "" {
			fingerprint = resp.Header.Get("Last-Modified")
		}
		return fingerprint, http.StatusOK, nil
	default:
		return "", resp.StatusCode, fmt.Errorf("unexpected status code %d from %s", resp.StatusCode, repoURL)
	}
}
