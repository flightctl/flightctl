package tasks

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/periodic"
	"github.com/flightctl/flightctl/internal/service/common"
	dependencyrefservice "github.com/flightctl/flightctl/internal/service/dependencyref"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	syncstateservice "github.com/flightctl/flightctl/internal/service/syncstate"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// httpConditionalHeadFunc is the injectable function type for testing.
// Returns: fingerprint (ETag or Last-Modified value), HTTP status code, error.
// Empty fingerprint with status 200 means the endpoint doesn't support
// conditional requests (no ETag or Last-Modified in response).
type httpConditionalHeadFunc func(ctx context.Context, client *http.Client, repoURL string,
	httpSpec domain.HttpRepoSpec, storedFingerprint string) (fingerprint string, statusCode int, err error)

type DependencySyncHttp struct {
	log              logrus.FieldLogger
	dependencyrefSvc dependencyrefservice.Service
	eventSvc         eventservice.Service
	syncstateSvc     syncstateservice.Service
	cfg              *config.Config
	conditionalHead  httpConditionalHeadFunc
	maxConcurrent    int
	metrics          *periodic.DependencySyncCollector
}

func NewDependencySyncHttp(log logrus.FieldLogger, dependencyrefSvc dependencyrefservice.Service, eventSvc eventservice.Service, syncstateSvc syncstateservice.Service,
	cfg *config.Config, metrics *periodic.DependencySyncCollector) *DependencySyncHttp {
	return &DependencySyncHttp{
		log:              log,
		dependencyrefSvc: dependencyrefSvc,
		eventSvc:         eventSvc,
		syncstateSvc:     syncstateSvc,
		cfg:              cfg,
		conditionalHead:  httpConditionalHead,
		maxConcurrent:    10,
		metrics:          metrics,
	}
}

type httpProbeResult struct {
	probe       *model.HttpDependencyProbe
	resourceKey string
	fingerprint string
	changed     bool
	firstSeen   bool
	probeErr    string
	skip        bool // true for HttpNotConditional or errors
}

func (d *DependencySyncHttp) Poll(ctx context.Context, orgId uuid.UUID) {
	if d.metrics != nil {
		d.metrics.ObserveProbeCycle(periodic.RefTypeHTTP)
	}

	pollInterval := d.cfg.GetDependenciesSyncPollInterval()
	probeStart := time.Now()

	probes, status := d.dependencyrefSvc.ListDueHttpDependencies(ctx, orgId, pollInterval)
	if status.Code != http.StatusOK {
		d.log.Errorf("failed listing due HTTP dependencies: %s", status.Message)
		return
	}
	if len(probes) == 0 {
		return
	}

	repoGroups := make(map[string][]*model.HttpDependencyProbe)
	for i := range probes {
		repoGroups[probes[i].RepositoryName] = append(repoGroups[probes[i].RepositoryName], &probes[i])
	}

	var (
		mu      sync.Mutex
		results []httpProbeResult
	)

	sem := make(chan struct{}, d.maxConcurrent)
	var wg sync.WaitGroup

	for _, group := range repoGroups {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res := d.probeRepoGroup(ctx, group)
			mu.Lock()
			results = append(results, res...)
			mu.Unlock()
		}()
	}

	wg.Wait()

	if d.metrics != nil {
		d.metrics.ObserveProbeLatency(periodic.RefTypeHTTP, time.Since(probeStart))
	}

	d.reconcile(ctx, orgId, results)
}

func (d *DependencySyncHttp) probeRepoGroup(ctx context.Context, group []*model.HttpDependencyProbe) []httpProbeResult {
	first := group[0]
	if first.RepoSpec == nil {
		var results []httpProbeResult
		for _, p := range group {
			d.log.Warnf("repository %s not found (no spec in JOIN result)", p.RepositoryName)
			results = append(results, httpProbeResult{probe: p, skip: true})
		}
		return results
	}

	httpSpec, err := first.RepoSpec.Data.AsHttpRepoSpec()
	if err != nil {
		var results []httpProbeResult
		for _, p := range group {
			d.log.WithError(err).Warnf("failed decoding HTTP spec for repository %s", p.RepositoryName)
			results = append(results, httpProbeResult{probe: p, skip: true})
		}
		return results
	}

	client, err := buildHTTPClient(httpSpec)
	if err != nil {
		var results []httpProbeResult
		for _, p := range group {
			d.log.WithError(err).Warnf("failed building HTTP client for repository %s", p.RepositoryName)
			results = append(results, httpProbeResult{probe: p, skip: true})
		}
		return results
	}

	var results []httpProbeResult
	for _, probe := range group {
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		r := d.probeEndpoint(probeCtx, client, httpSpec, probe)
		cancel()
		results = append(results, r)
	}
	return results
}

func (d *DependencySyncHttp) probeEndpoint(ctx context.Context, client *http.Client,
	httpSpec domain.HttpRepoSpec, probe *model.HttpDependencyProbe) httpProbeResult {

	repoURL, err := url.JoinPath(httpSpec.Url, probe.HTTPSuffix)
	if err != nil {
		d.log.WithError(err).Warnf("failed joining URL for repository %s", probe.RepositoryName)
		return httpProbeResult{probe: probe, skip: true}
	}
	rk := httpResourceKey(probe.RepositoryName, probe.HTTPSuffix)

	storedFP := ""
	if probe.Fingerprint != nil {
		storedFP = *probe.Fingerprint
	}

	fingerprint, statusCode, err := d.conditionalHead(ctx, client, repoURL, httpSpec, storedFP)
	if err != nil {
		d.log.WithError(err).Warnf("HTTP probe failed for %s (status %d)", rk, statusCode)
		if d.metrics != nil {
			d.metrics.ObserveProbeError(periodic.RefTypeHTTP)
		}
		return httpProbeResult{probe: probe, resourceKey: rk, skip: true, probeErr: sanitizeError(err)}
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
		if r.probeErr != "" {
			for _, fleetName := range r.probe.FleetNames {
				event := common.GetDependencySyncProbeFailedEvent(ctx, domain.FleetKind, fleetName, r.resourceKey, r.probeErr)
				if event != nil {
					d.eventSvc.CreateEvent(ctx, orgId, event)
				}
			}
			for _, deviceName := range r.probe.DeviceNames {
				event := common.GetDependencySyncProbeFailedEvent(ctx, domain.DeviceKind, deviceName, r.resourceKey, r.probeErr)
				if event != nil {
					d.eventSvc.CreateEvent(ctx, orgId, event)
				}
			}
			continue
		}
		if !r.changed {
			continue
		}
		if d.metrics != nil {
			d.metrics.ObserveProbeChange(periodic.RefTypeHTTP)
		}
		for _, fleetName := range r.probe.FleetNames {
			event := common.GetDependencyChangeDetectedEvent(ctx, domain.FleetKind, fleetName, r.resourceKey, r.fingerprint)
			if event != nil {
				d.eventSvc.CreateEvent(ctx, orgId, event)
			}
		}
		for _, deviceName := range r.probe.DeviceNames {
			event := common.GetDependencyChangeDetectedEvent(ctx, domain.DeviceKind, deviceName, r.resourceKey, r.fingerprint)
			if event != nil {
				d.eventSvc.CreateEvent(ctx, orgId, event)
			}
		}
	}

	var upsertStates []model.SyncState
	var unchangedKeys []string

	for _, r := range results {
		if r.resourceKey == "" {
			continue
		}
		if r.probeErr != "" {
			upsertStates = append(upsertStates, model.SyncState{
				OrgID:         orgId,
				ResourceKey:   r.resourceKey,
				ProbeStatus:   "ProbeFailed",
				ProbeMessage:  r.probeErr,
				LastCheckedAt: now,
			})
			continue
		}
		if r.skip {
			unchangedKeys = append(unchangedKeys, r.resourceKey)
		} else if r.firstSeen || r.changed {
			upsertStates = append(upsertStates, model.SyncState{
				OrgID:         orgId,
				ResourceKey:   r.resourceKey,
				Fingerprint:   r.fingerprint,
				ProbeStatus:   "Synced",
				LastCheckedAt: now,
				LastChangeAt:  &now,
			})
		} else {
			unchangedKeys = append(unchangedKeys, r.resourceKey)
		}
	}

	if len(upsertStates) > 0 {
		if st := d.syncstateSvc.BulkUpsertSyncState(ctx, orgId, upsertStates); st.Code != http.StatusOK {
			d.log.Errorf("failed bulk upserting sync states: %s", st.Message)
			return
		}
	}

	if len(unchangedKeys) > 0 {
		if st := d.syncstateSvc.BulkUpdateSyncStateLastCheckedAt(ctx, orgId, unchangedKeys, now); st.Code != http.StatusOK {
			d.log.Errorf("failed bulk updating last_checked_at: %s", st.Message)
		}
	}
}

func httpResourceKey(repoName, suffix string) string {
	return fmt.Sprintf("http:%s/%s", repoName, strings.TrimPrefix(suffix, "/"))
}

func buildHTTPClient(httpSpec domain.HttpRepoSpec) (*http.Client, error) {
	req, err := http.NewRequest("HEAD", "http://unused", nil)
	if err != nil {
		return nil, err
	}
	_, tlsConfig, err := buildHttpRepoRequestAuth(httpSpec, req)
	if err != nil {
		return nil, fmt.Errorf("building TLS config: %w", err)
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}, nil
}

// httpConditionalHead sends a conditional HEAD to the given URL using stored
// fingerprint for If-None-Match/If-Modified-Since headers. HEAD avoids
// transferring the response body since we only need ETag/Last-Modified headers.
func httpConditionalHead(ctx context.Context, client *http.Client, repoURL string,
	httpSpec domain.HttpRepoSpec, storedFingerprint string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, "HEAD", repoURL, nil)
	if err != nil {
		return "", 0, fmt.Errorf("creating request: %w", err)
	}

	req, _, err = buildHttpRepoRequestAuth(httpSpec, req)
	if err != nil {
		return "", 0, fmt.Errorf("building request auth: %w", err)
	}

	if storedFingerprint != "" {
		// ETags always start with `"` or `W/"` (RFC 7232 §2.3), while
		// Last-Modified is an HTTP-date. We must send the correct header
		// for the stored fingerprint type: If-None-Match for ETags,
		// If-Modified-Since for dates. Sending an ETag as If-Modified-Since
		// (or vice versa) is a protocol violation that may cause servers to
		// ignore the condition or return 400.
		if strings.HasPrefix(storedFingerprint, `"`) || strings.HasPrefix(storedFingerprint, `W/"`) {
			req.Header.Set("If-None-Match", storedFingerprint)
		} else {
			req.Header.Set("If-Modified-Since", storedFingerprint)
		}
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
