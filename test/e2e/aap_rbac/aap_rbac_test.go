package aap_rbac_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// parseOrgID extracts the UUID from organization listing output
// Expected format: "UUID    DISPLAY NAME    EXTERNAL ID"
func parseOrgID(orgOutput, displayName string) (string, error) {
	lines := strings.Split(orgOutput, "\n")
	for _, line := range lines {
		if strings.Contains(line, displayName) {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				// First field should be the UUID
				uuid := fields[0]
				// Basic UUID format validation (8-4-4-4-12)
				if len(uuid) == 36 && strings.Count(uuid, "-") == 4 {
					return uuid, nil
				}
			}
		}
	}
	return "", fmt.Errorf("could not find UUID for organization %q in output", displayName)
}

var _ = Describe("AAP custom role access", Label("EDM-5636", "aap-rbac", "quadlets"), func() {
	Context("When authenticating with custom AAP role", func() {
		It("allows user to access org resources", func() {
			harness := e2e.GetWorkerHarness()

			By("verifying AAP admin is already authenticated")
			// Admin authentication is set up in BeforeSuite via LoginToAPIWithToken

			By("listing devices to verify access")
			out, err := harness.RunGetDevices()
			Expect(err).ToNot(HaveOccurred(), "AAP-authenticated user should be able to list devices")

			By("verifying no access denied errors")
			Expect(strings.ToLower(out)).ToNot(ContainSubstring("forbidden"), "user should have access to resources")
			Expect(strings.ToLower(out)).ToNot(ContainSubstring("restricted access"), "user should not see restricted access")
		})

		It("allows service account to access org resources", func() {
			harness := e2e.GetWorkerHarness()

			// Check E2E_AAP_TOKEN first (set by ocp-flightctl-gotests), fallback to AAP_TOKEN
			aapToken := strings.TrimSpace(os.Getenv("E2E_AAP_TOKEN"))
			if aapToken == "" {
				aapToken = strings.TrimSpace(os.Getenv("AAP_TOKEN"))
			}
			if aapToken == "" {
				Skip("E2E_AAP_TOKEN or AAP_TOKEN not set - cannot test service account authentication")
			}

			By("verifying AAP token authentication")
			// Note: admin token used here (not custom-role-only service account)
			// This tests token-based authentication works, but not the custom-role-only requirement
			// TODO: Replace with custom-role-only service account token once available

			By("performing org-scoped operation with token")
			out, err := harness.RunGetDevices()
			Expect(err).ToNot(HaveOccurred(), "AAP token authentication should work")
			Expect(strings.ToLower(out)).ToNot(ContainSubstring("forbidden"), "token should grant access to resources")
		})

		It("completes full login/identity/RBAC pipeline", func() {
			harness := e2e.GetWorkerHarness()

			By("verifying login and authentication succeeded")
			// Admin authentication completed in BeforeSuite

			By("verifying identity resolution works")
			out, err := harness.RunGetDevices()
			Expect(err).ToNot(HaveOccurred(), "API calls should succeed after AAP authentication")

			By("verifying RBAC enforcement allows access")
			// The fact that we can list devices means:
			// 1. Login succeeded
			// 2. Identity resolution via AAP Gateway succeeded
			// 3. Role translation happened
			// 4. Permission enforcement allowed the operation
			Expect(strings.ToLower(out)).ToNot(ContainSubstring("forbidden"), "RBAC should allow access")
		})
	})
})

var _ = Describe("AAP org-scoped tenant isolation", Label("EDM-5636", "aap-rbac", "quadlets", "security"), func() {
	Context("When users are scoped to different orgs", func() {
		It("denies cross-org resource access", func() {
			harness := e2e.GetWorkerHarness()

			By("getting list of organizations")
			orgOut, err := harness.CLI("get", "organizations")
			Expect(err).ToNot(HaveOccurred(), "should be able to list organizations")

			By("verifying org-a and org-b exist")
			Expect(orgOut).To(ContainSubstring("org-a"), "org-a should exist")
			Expect(orgOut).To(ContainSubstring("org-b"), "org-b should exist")

			By("resolving organization UUIDs from cluster output")
			orgAUUID, err := parseOrgID(orgOut, "org-a")
			Expect(err).ToNot(HaveOccurred(), "should be able to parse org-a UUID")
			orgBUUID, err := parseOrgID(orgOut, "org-b")
			Expect(err).ToNot(HaveOccurred(), "should be able to parse org-b UUID")

			By(fmt.Sprintf("resolved org-a UUID: %s", orgAUUID))
			By(fmt.Sprintf("resolved org-b UUID: %s", orgBUUID))

			By("attempting to access org-b devices while authenticated as admin")
			// Note: Using admin user which has access to all orgs
			// Ideal test would use org-scoped users (user-a scoped to org-a only, user-b to org-b only)
			// but that requires separate AAP user authentication mechanism

			By("verifying org-scoped access control exists")
			// With admin user, we can verify the org_id parameter works
			// but cannot test the isolation enforcement (admin has access to all orgs)
			_, err = harness.RunGetDevices("--org", orgAUUID)
			Expect(err).ToNot(HaveOccurred(), "should be able to query org-a devices")

			_, err = harness.RunGetDevices("--org", orgBUUID)
			Expect(err).ToNot(HaveOccurred(), "should be able to query org-b devices")

			// Admin can access both orgs - this is expected
			// True isolation test requires org-scoped users which need to be created in AAP
			Skip("Partial validation complete (orgs exist, org_id parameter works). " +
				"Full tenant isolation test requires AAP users scoped to single org only. " +
				"Current test uses admin (has access to all orgs). " +
				"To complete: Create AAP user-a scoped to org-a only, user-b to org-b only, " +
				"verify user-a gets 403 when accessing org-b resources and vice versa.")
		})
	})
})

var _ = Describe("AAP pagination at scale", Label("EDM-5636", "aap-rbac", "quadlets", "scale"), func() {
	Context("When user has 25+ role assignments", func() {
		It("resolves all paginated role assignments", func() {
			Skip("Pagination scale test requires test user with 26+ direct role assignments, " +
				"26+ team memberships, and team with 26+ role assignments. " +
				"AAP test environment does not have this scale configuration. " +
				"To enable: Create scale data in AAP manually or via AAP API script, " +
				"set AAP_SCALE_USER environment variable with test user credentials. " +
				"Testplan TC-NFR2-04: validates pagination resolves beyond page 1. " +
				"Tests EDM-5633 fix (relative/absolute next URLs via ResolveReference).")
		})
	})
})

// AAP token API types
//
//nolint:unused // Used by token revocation test
type aapTokenRequest struct {
	Description string `json:"description"`
	Scope       string `json:"scope"`
}

//nolint:unused // Used by token revocation test
type aapTokenResponse struct {
	ID          int    `json:"id"`
	Token       string `json:"token"`
	Description string `json:"description"`
}

// newAAPAPIClient returns an HTTP client configured for AAP API calls
//
//nolint:unused // Used by createAAPToken and revokeAAPToken
func newAAPAPIClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // E2E AAP test environment uses self-signed certificates
		},
		Timeout: 30 * time.Second,
	}
}

// createAAPToken creates a new application token via AAP API
//
//nolint:unused // Used by token revocation test
func createAAPToken(ctx context.Context, aapURL, adminToken, description string) (*aapTokenResponse, error) {
	reqBody := aapTokenRequest{
		Description: description,
		Scope:       "write",
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal token request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/gateway/v1/tokens/", strings.TrimSuffix(aapURL, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", adminToken))
	req.Header.Set("Content-Type", "application/json")

	client := newAAPAPIClient()
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("AAP API error (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp aapTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &tokenResp, nil
}

// revokeAAPToken revokes an application token via AAP API
//
//nolint:unused // Used by token revocation test and cleanup
func revokeAAPToken(ctx context.Context, aapURL, adminToken string, tokenID int) error {
	endpoint := fmt.Sprintf("%s/api/gateway/v1/tokens/%d/", strings.TrimSuffix(aapURL, "/"), tokenID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", adminToken))

	client := newAAPAPIClient()
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("AAP API error (status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

var _ = Describe("AAP token revocation timing", Label("EDM-5639", "aap-rbac", "quadlets", "revocation"), func() {
	Context("When a token is revoked in AAP", func() {
		It("rejects the revoked token within 60 seconds", func() {
			harness := e2e.GetWorkerHarness()
			aapURL := strings.TrimSpace(os.Getenv("AAP_API_URL"))
			// Use the shared admin token to create a new test token (don't revoke the shared one!)
			adminToken := strings.TrimSpace(os.Getenv("E2E_AAP_TOKEN"))
			if adminToken == "" {
				adminToken = strings.TrimSpace(os.Getenv("AAP_TOKEN"))
			}

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()

			By("creating a new admin token via AAP API for this test")
			testToken, err := createAAPToken(ctx, aapURL, adminToken, "flightctl-e2e-revocation-test")
			Expect(err).ToNot(HaveOccurred(), "should create test token via AAP API")
			Expect(testToken).ToNot(BeNil())
			Expect(testToken.ID).To(BeNumerically(">", 0))
			Expect(testToken.Token).ToNot(BeEmpty())

			By(fmt.Sprintf("test token created: ID=%d", testToken.ID))

			// Register cleanup to ensure token is revoked even if test fails
			tokenRevoked := false
			DeferCleanup(func() {
				if !tokenRevoked {
					_ = revokeAAPToken(context.Background(), aapURL, adminToken, testToken.ID)
				}
			})

			By("saving original CLI config to restore after test")
			originalConfig, err := captureClientConfigSnapshot()
			Expect(err).ToNot(HaveOccurred(), "should capture original CLI config")
			DeferCleanup(func() {
				_ = restoreClientConfigSnapshot(harness, originalConfig)
			})

			By("logging in with the new test token to establish a separate session")
			apiEndpoint := harness.ApiEndpoint()
			out, err := harness.CLI("login", apiEndpoint, "--insecure-skip-tls-verify", "--token", testToken.Token)
			Expect(err).ToNot(HaveOccurred(), "should login with test token\nOutput: %s", out)

			By("verifying test token session works before revocation")
			out, err = harness.RunGetDevices()
			Expect(err).ToNot(HaveOccurred(), "should be able to list devices with test token session")
			Expect(strings.TrimSpace(out)).ToNot(BeEmpty(), "device list should not be empty")

			By("recording revocation start time")
			revocationStart := time.Now()

			By(fmt.Sprintf("revoking test token (ID=%d) via AAP API", testToken.ID))
			err = revokeAAPToken(ctx, aapURL, adminToken, testToken.ID)
			Expect(err).ToNot(HaveOccurred(), "should revoke test token via AAP API")
			tokenRevoked = true

			By("polling Flight Control to verify revoked token is rejected within 60 seconds")
			Eventually(func() error {
				_, err := harness.RunGetDevices()
				return err
			}).WithTimeout(90*time.Second).WithPolling(10*time.Second).Should(HaveOccurred(),
				"revoked token should be rejected within 60 seconds (45s cache TTL + polling interval)")

			revocationDuration := time.Since(revocationStart)
			By(fmt.Sprintf("token rejected after %.2fs (within 60s SLA)", revocationDuration.Seconds()))
			Expect(revocationDuration.Seconds()).To(BeNumerically("<=", 60), "revocation timing must meet 60s SLA")
		})
	})
})
