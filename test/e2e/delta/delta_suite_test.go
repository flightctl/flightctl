package delta

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	e2eredis "github.com/flightctl/flightctl/test/e2e/infra/redis"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDelta(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "OS Delta E2E Suite")
}

var auxSvcs *auxiliary.Services

var _ = BeforeSuite(func() {
	auxFuture := e2e.StartAuxServicesAsync(context.Background())
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	e2e.SetupWorkerHarnessOrAbort()
	auxSvcs = auxFuture.Wait()
})

var _ = AfterSuite(func() {
	if auxSvcs != nil {
		auxSvcs.Cleanup(context.Background())
	}
})

var _ = BeforeEach(func() {
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	_, err := login.LoginToAPIWithToken(harness)
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("[BeforeEach] Worker %d: Setting up test with VM from pool\n", workerID)

	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)
	Expect(clearDeltaCache(ctx)).To(Succeed())

	err = harness.SetupVMFromPoolAndStartAgent(workerID)
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("[BeforeEach] Worker %d: Test setup completed\n", workerID)
})

var _ = AfterEach(func() {
	workerID := GinkgoParallelProcess()
	GinkgoWriter.Printf("[AfterEach] Worker %d: Cleaning up test resources\n", workerID)

	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	harness.PrintAgentLogsIfFailed()
	harness.CaptureDeploymentLogsIfFailed()

	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())

	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("[AfterEach] Worker %d: Test cleanup completed\n", workerID)
})

func clearDeltaCache(ctx context.Context) error {
	if err := clearDeltaGenerations(); err != nil {
		return err
	}
	if err := clearDeltaHintKeys(ctx); err != nil {
		return err
	}
	return clearRegistryDeltaTags(ctx)
}

func clearDeltaGenerations() error {
	p := setup.GetDefaultProviders()
	if p == nil || p.Infra == nil {
		return fmt.Errorf("providers not initialized")
	}
	if !p.Infra.BuiltinDatabaseWorkloadAvailable() {
		return fmt.Errorf("built-in flightctl database is required to clear delta generations")
	}
	_, err := infra.QueryDB(p, "TRUNCATE TABLE delta_prepare_generations, delta_prepares, delta_generations")
	if err != nil {
		return fmt.Errorf("truncate delta tables: %w", err)
	}
	return nil
}

func clearDeltaHintKeys(ctx context.Context) error {
	client, cleanup, err := e2eredis.GetRedisClient()
	if err != nil {
		return fmt.Errorf("redis client: %w", err)
	}
	defer cleanup()

	var cursor uint64
	for {
		keys, next, err := client.Scan(ctx, cursor, "deltaHint/*", 100).Result()
		if err != nil {
			return fmt.Errorf("scan deltaHint keys: %w", err)
		}
		if len(keys) > 0 {
			if err := client.Del(ctx, keys...).Err(); err != nil {
				return fmt.Errorf("delete deltaHint keys: %w", err)
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

const registryManifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json"

func clearRegistryDeltaTags(ctx context.Context) error {
	if auxSvcs == nil || auxSvcs.Registry == nil || auxSvcs.Registry.URL == "" {
		return fmt.Errorf("e2e registry is not started")
	}
	client, err := e2eRegistryHTTPClient()
	if err != nil {
		return err
	}
	base := "https://" + auxSvcs.Registry.URL
	repo := testutil.DeviceImageRegistryPath
	tags, err := listRegistryTags(ctx, client, base, repo)
	if err != nil {
		return err
	}
	for _, tag := range tags {
		if !strings.HasPrefix(tag, "sha256-") {
			continue
		}
		if err := deleteRegistryTag(ctx, client, base, repo, tag); err != nil {
			return err
		}
	}
	return nil
}

func e2eRegistryHTTPClient() (*http.Client, error) {
	caPEM, err := os.ReadFile(filepath.Join(testutil.GetTopLevelDir(), "bin", "e2e-certs", "pki", "CA", "ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("read e2e registry CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse e2e registry CA")
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}, nil
}

func listRegistryTags(ctx context.Context, client *http.Client, base, repo string) ([]string, error) {
	var tags []string
	next := base + "/v2/" + repo + "/tags/list?n=100"
	for next != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list registry tags: %w", err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("list registry tags: %w", err)
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("list registry tags: %s: %s", resp.Status, strings.TrimSpace(string(body)))
		}
		var listed struct {
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(body, &listed); err != nil {
			return nil, fmt.Errorf("list registry tags: %w", err)
		}
		tags = append(tags, listed.Tags...)
		next = registryLinkURL(resp.Header.Get("Link"), base)
	}
	return tags, nil
}

func registryLinkURL(link, base string) string {
	if link == "" {
		return ""
	}
	start := strings.Index(link, "<")
	end := strings.Index(link, ">")
	if start < 0 || end <= start {
		return ""
	}
	href := link[start+1 : end]
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	return base + href
}

func deleteRegistryTag(ctx context.Context, client *http.Client, base, repo, tag string) error {
	digest, err := registryManifestDigest(ctx, client, base, repo, tag)
	if err != nil {
		return err
	}
	if digest == "" {
		return nil
	}
	url := base + "/v2/" + repo + "/manifests/" + digest
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("delete registry tag %s: %w", tag, err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return fmt.Errorf("delete registry tag %s: %w", tag, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode == http.StatusMethodNotAllowed {
		return fmt.Errorf("delete registry tag %s: storage delete is disabled; recreate the e2e-registry container so REGISTRY_STORAGE_DELETE_ENABLED=true takes effect", tag)
	}
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("delete registry tag %s: %s: %s", tag, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func registryManifestDigest(ctx context.Context, client *http.Client, base, repo, ref string) (string, error) {
	url := base + "/v2/" + repo + "/manifests/" + ref
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", registryManifestAccept)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("head registry tag %s: %w", ref, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("head registry tag %s: %s", ref, resp.Status)
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("head registry tag %s: missing Docker-Content-Digest", ref)
	}
	return digest, nil
}
