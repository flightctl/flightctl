package auxiliary

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/test/util"
	"github.com/sirupsen/logrus"
)

// TEMPORARY DIAGNOSTIC (remove once root-caused): a specific CI shard
// (e2e-tests cs10-bootc.../e2e-test (3), Rootless applications spec) has reproduced,
// on back-to-back runs, a device stuck getting "connection refused" pulling
// quay.io/flightctl-tests/* through the local registry remap for the device's entire
// lifetime - even though the same host/IP is reachable on other ports (API server) at
// the same time. This monitor periodically checks, from the runner's own perspective,
// whether the registry's advertised address is actually reachable and what state the
// underlying container/port publishing are in, so the next reproduction tells us
// definitively whether the container crashed, was never bound correctly, or something
// else. Output goes to both logrus (so it's visible in the live job log) and a file
// under artifacts/deployment-logs/ (uploaded as a CI artifact regardless of pass/fail,
// unlike Ginkgo's per-spec captured output which is only shown for failing specs).
const (
	registryHealthCheckInterval = 10 * time.Second
	registryHealthCheckDuration = 25 * time.Minute
)

var (
	registryHealthMonitorOnce sync.Once
	diagnosticFileOnce        sync.Once
	diagnosticFile            *os.File
	diagnosticMu              sync.Mutex
)

// logDiagnostic appends a timestamped line to artifacts/deployment-logs/registry-health.log
// (created on first use) and to logrus, so phase markers (upload/mirror start & end) and
// health-probe results share one file and can be correlated by timestamp. See the
// TEMPORARY DIAGNOSTIC note above startHealthMonitor for why this exists.
func logDiagnostic(format string, args ...interface{}) {
	diagnosticFileOnce.Do(func() {
		logPath := filepath.Join(util.GetTopLevelDir(), "artifacts", "deployment-logs", "registry-health.log")
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			logrus.Warnf("[registry-health] failed to create diagnostic log dir: %v", err)
			return
		}
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			logrus.Warnf("[registry-health] failed to open diagnostic log %s: %v", logPath, err)
			return
		}
		diagnosticFile = f
	})

	line := fmt.Sprintf(format, args...)
	logrus.Info("[registry-health] " + line)
	if diagnosticFile == nil {
		return
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	diagnosticMu.Lock()
	defer diagnosticMu.Unlock()
	if _, err := fmt.Fprintf(diagnosticFile, "%s %s\n", ts, line); err != nil {
		logrus.Warnf("[registry-health] write failed: %v", err)
	}
}

// startHealthMonitor launches a background goroutine that periodically probes this
// registry's reachability and container/port state. Safe to call multiple times
// (across StartServices invocations in different Ginkgo parallel workers within the
// same process) - only the first call actually starts the monitor.
func (r *Registry) startHealthMonitor(ctx context.Context) {
	registryHealthMonitorOnce.Do(func() {
		r.runHealthMonitor(ctx)
	})
}

// StartStandaloneRegistryHealthMonitor starts the same monitor as startHealthMonitor,
// but computes the registry's address itself instead of requiring a *Registry from
// Get/StartServices. Some suites (e.g. rootless, see rootless_suite_test.go) never
// call auxiliary.Get() at all - they only set up a VM/harness and rely on a registry
// container created by a *different* suite process earlier in the same CI job (the
// container outlives the process that created it - see reuse=true/SkipReaper). Those
// suites otherwise get zero visibility from this diagnostic. Safe to call even if
// this process also calls Get() elsewhere; only the first monitor actually starts.
func StartStandaloneRegistryHealthMonitor(ctx context.Context) {
	hostIP := GetHostIP()
	r := &Registry{Port: registryHostPort}
	if strings.Contains(hostIP, ":") {
		r.URL = fmt.Sprintf("localhost:%s", r.Port)
		r.Host = registryContainerName
	} else {
		r.Host = hostIP
		r.URL = fmt.Sprintf("%s:%s", r.Host, r.Port)
	}
	r.Authenticated = AuthenticatedEndpoint{
		HostPort: net.JoinHostPort(r.Host, privateRegistryHostPort),
		Port:     privateRegistryHostPort,
		Username: defaultAuthUsername,
		Password: defaultAuthPassword,
	}
	r.startHealthMonitor(ctx)
}

func (r *Registry) runHealthMonitor(ctx context.Context) {
	logDiagnostic("monitor started url=%s host=%s port=%s reused=%v", r.URL, r.Host, r.Port, r.Reused)

	go func() {
		client := &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				//nolint:gosec // diagnostic-only probe; matches the registry's own WithAllowInsecure health check
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
		cli := containerRuntimeCLIName()
		deadline := time.Now().Add(registryHealthCheckDuration)

		probe := func(name, url string, withAuth bool) {
			start := time.Now()
			req, err := http.NewRequest(http.MethodGet, url, nil)
			if err != nil {
				logDiagnostic("%s GET %s FAILED building request: %v", name, url, err)
				return
			}
			if withAuth {
				req.SetBasicAuth(r.Authenticated.Username, r.Authenticated.Password)
			}
			resp, getErr := client.Do(req)
			elapsed := time.Since(start)
			if getErr != nil {
				logDiagnostic("%s GET %s FAILED after %s: %v", name, url, elapsed, getErr)
				return
			}
			logDiagnostic("%s GET %s OK status=%d after %s", name, url, resp.StatusCode, elapsed)
			_ = resp.Body.Close()
		}

		check := func() {
			// Direct: hits the registry container's own published port (5000).
			probe("registry", fmt.Sprintf("https://%s/v2/", r.URL), false)
			// Indirect: nginx (a *different* container) proxies to the SAME published
			// host:5000 address (see generateNginxConf) before exposing 5002 with basic
			// auth. If 5000 breaks while 5002 stays up, the registry process itself is
			// fine and something is specific to how 5000 is published/reachable. If both
			// break together, it's likely the host's port-publishing layer in general
			// (e.g. rootlessport), not the registry process.
			if r.Authenticated.HostPort != "" {
				probe("registry-auth", fmt.Sprintf("https://%s/v2/", r.Authenticated.HostPort), true)
			}

			if out, cmdErr := exec.Command(cli, "ps", "-a", //nolint:gosec // cli is podman|docker; container names are fixed package constants
				"--filter", "name=^"+registryContainerName+"$",
				"--filter", "name=^"+privateRegistryContainerName+"$",
				"--format", "{{.Names}} {{.Status}}").CombinedOutput(); cmdErr != nil {
				logDiagnostic("%s ps failed: %v output=%s", cli, cmdErr, strings.TrimSpace(string(out)))
			} else {
				logDiagnostic("%s ps: %s", cli, strings.TrimSpace(string(out)))
			}

			if out, cmdErr := exec.Command(cli, "port", registryContainerName).CombinedOutput(); cmdErr != nil { //nolint:gosec // see above
				logDiagnostic("%s port %s failed: %v output=%s", cli, registryContainerName, cmdErr, strings.TrimSpace(string(out)))
			} else {
				logDiagnostic("%s port %s: %s", cli, registryContainerName, strings.TrimSpace(string(out)))
			}

			if out, cmdErr := exec.Command(cli, "port", privateRegistryContainerName).CombinedOutput(); cmdErr != nil { //nolint:gosec // see above
				logDiagnostic("%s port %s failed: %v output=%s", cli, privateRegistryContainerName, cmdErr, strings.TrimSpace(string(out)))
			} else {
				logDiagnostic("%s port %s: %s", cli, privateRegistryContainerName, strings.TrimSpace(string(out)))
			}
		}

		check() // immediate baseline, before returning control to the caller (e.g. before mirroring starts)
		ticker := time.NewTicker(registryHealthCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				logDiagnostic("monitor stopping: context cancelled")
				return
			case <-ticker.C:
				if time.Now().After(deadline) {
					logDiagnostic("monitor stopping: deadline reached")
					return
				}
				check()
			}
		}
	}()
}
