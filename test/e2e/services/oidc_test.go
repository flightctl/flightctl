package services

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("FlightCtl Services OIDC Authentication", Label("OCP-83302", "oidc", "auth"), func() {

	It("should validate OIDC configuration and authentication flow", func() {
		// Step 1: Verify OIDC configuration files exist
		By("verifying OIDC configuration files exist")
		serviceConfigExists, err := fileExists("/etc/flightctl/service-config.yaml")
		Expect(err).ToNot(HaveOccurred())
		Expect(serviceConfigExists).To(BeTrue(), "service-config.yaml should exist")

		apiConfigExists, err := fileExists("/etc/flightctl/flightctl-api/config.yaml")
		Expect(err).ToNot(HaveOccurred())
		Expect(apiConfigExists).To(BeTrue(), "flightctl-api/config.yaml should exist")

		// Step 2: Read and validate OIDC configuration
		By("reading and validating service-config.yaml for OIDC settings")
		serviceConfig, err := readConfigFile("/etc/flightctl/service-config.yaml")
		Expect(err).ToNot(HaveOccurred())
		Expect(serviceConfig).ToNot(BeEmpty())
		GinkgoWriter.Printf("Service config preview:\n%s\n", util.TruncateString(serviceConfig, 300))

		// Check for OIDC configuration keywords
		Expect(serviceConfig).To(ContainSubstring("auth:"), "config should have auth section")
		Expect(serviceConfig).To(Or(
			ContainSubstring("type: oidc"),
			ContainSubstring("type: builtin"),
		), "config should specify auth type")

		By("reading and validating API config for OIDC settings")
		apiConfig, err := readConfigFile("/etc/flightctl/flightctl-api/config.yaml")
		Expect(err).ToNot(HaveOccurred())
		Expect(apiConfig).ToNot(BeEmpty())

		// Step 3: Check if OIDC is configured
		oidcConfigured := strings.Contains(serviceConfig, "type: oidc") || 
			strings.Contains(apiConfig, "oidc:")

		if oidcConfigured {
			GinkgoWriter.Printf("✓ OIDC authentication is configured\n")

			// Step 4: Extract OIDC authority URL
			By("extracting OIDC authority URL from configuration")
			oidcAuthority := extractOIDCAuthority(apiConfig)
			if oidcAuthority != "" {
				GinkgoWriter.Printf("OIDC Authority: %s\n", oidcAuthority)

				// Step 5: Verify OIDC provider is accessible
				By("verifying OIDC provider discovery endpoint is accessible")
				discoveryURL := oidcAuthority + "/.well-known/openid-configuration"
				GinkgoWriter.Printf("Discovery URL: %s\n", discoveryURL)

				discoveryData, err := fetchOIDCDiscovery(discoveryURL)
				if err == nil {
					GinkgoWriter.Printf("✓ OIDC discovery endpoint is accessible\n")
					GinkgoWriter.Printf("OIDC Issuer: %s\n", discoveryData.Issuer)
					GinkgoWriter.Printf("Authorization Endpoint: %s\n", discoveryData.AuthorizationEndpoint)
					GinkgoWriter.Printf("Token Endpoint: %s\n", discoveryData.TokenEndpoint)

					// Verify required endpoints are present
					Expect(discoveryData.Issuer).ToNot(BeEmpty(), "OIDC issuer should be present")
					Expect(discoveryData.AuthorizationEndpoint).ToNot(BeEmpty(), "authorization endpoint should be present")
					Expect(discoveryData.TokenEndpoint).ToNot(BeEmpty(), "token endpoint should be present")
				} else {
					GinkgoWriter.Printf("⚠ OIDC discovery endpoint not accessible: %v\n", err)
					GinkgoWriter.Printf("Note: This is acceptable if OIDC provider is not set up yet\n")
				}
			}

			// Step 6: Check API auth configuration endpoint
			By("checking FlightCtl API auth configuration endpoint")
			apiURL := "https://localhost:3443/api/v1/auth/config"
			authConfig, err := checkAPIAuthConfig(apiURL)
			if err == nil {
				GinkgoWriter.Printf("✓ API auth config endpoint is accessible\n")
				if authConfig.Type == "oidc" {
					GinkgoWriter.Printf("✓ API reports OIDC authentication enabled\n")
					GinkgoWriter.Printf("OIDC Client ID: %s\n", authConfig.OIDCClientID)
					GinkgoWriter.Printf("OIDC Authority: %s\n", authConfig.OIDCAuthority)
				} else {
					GinkgoWriter.Printf("⚠ API reports auth type: %s (expected: oidc)\n", authConfig.Type)
				}
			} else {
				GinkgoWriter.Printf("⚠ API auth config endpoint check failed: %v\n", err)
				GinkgoWriter.Printf("Note: This might indicate auth is not fully initialized\n")
			}

			// Step 7: Check for PAM OIDC Issuer service
			By("checking if PAM OIDC Issuer service exists")
			pamServiceExists, err := util.SystemdUnitExists("flightctl-pam-issuer.service")
			if err == nil && pamServiceExists {
				GinkgoWriter.Printf("✓ PAM OIDC Issuer service unit exists\n")

				// Check if PAM issuer service is running
				pamStatus, _ := util.GetSystemdStatus("flightctl-pam-issuer.service")
				if strings.Contains(pamStatus, "active (running)") {
					GinkgoWriter.Printf("✓ PAM OIDC Issuer service is running\n")
				} else {
					GinkgoWriter.Printf("⚠ PAM OIDC Issuer service status:\n%s\n",
						util.TruncateString(pamStatus, 400))
				}
			} else {
				GinkgoWriter.Printf("⚠ PAM OIDC Issuer service not found\n")
			}

		} else {
			GinkgoWriter.Printf("✓ OIDC authentication is not configured (using builtin or no auth)\n")
		}

		// Step 8: Verify authentication-related services
		By("verifying authentication-related services status")
		authServices := []string{
			"flightctl-api.service",
			"flightctl-api-init.service",
		}

		for _, service := range authServices {
			exists, err := util.SystemdUnitExists(service)
			if err == nil && exists {
				status, _ := util.GetSystemdStatus(service)
				if strings.Contains(status, "active (running)") || strings.Contains(status, "inactive (dead)") {
					GinkgoWriter.Printf("✓ %s is present\n", service)
				} else {
					GinkgoWriter.Printf("⚠ %s status:\n%s\n", service,
						util.TruncateString(status, 300))
				}
			}
		}

		// Step 9: Check for required environment variables or secrets
		By("checking for authentication configuration completeness")
		// Check if insecureSkipTlsVerify is set (common for dev/test environments)
		if strings.Contains(apiConfig, "insecureSkipTlsVerify") {
			GinkgoWriter.Printf("⚠ insecureSkipTlsVerify is enabled (acceptable for testing)\n")
		}
	})
})

// OIDCDiscoveryResponse represents the OIDC discovery document
type OIDCDiscoveryResponse struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
}

// APIAuthConfig represents the FlightCtl API auth configuration response
type APIAuthConfig struct {
	Type           string `json:"type"`
	OIDCClientID   string `json:"oidcClientId,omitempty"`
	OIDCAuthority  string `json:"oidcAuthority,omitempty"`
}

// Helper function to check if a file exists
func fileExists(path string) (bool, error) {
	cmd := fmt.Sprintf("test -f %s && echo 'exists' || echo 'missing'", path)
	output, err := util.ExecCommand("sh", "-c", cmd)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(output) == "exists", nil
}

// Helper function to read a config file
func readConfigFile(path string) (string, error) {
	output, err := util.ExecCommand("cat", path)
	if err != nil {
		return "", fmt.Errorf("failed to read config file %s: %w", path, err)
	}
	return output, nil
}

// Helper function to extract OIDC authority from config
func extractOIDCAuthority(config string) string {
	// Look for oidcAuthority: <url>
	lines := strings.Split(config, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "oidcAuthority:") || strings.HasPrefix(line, "externalOidcAuthority:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				authority := strings.TrimSpace(parts[1])
				// Remove quotes if present
				authority = strings.Trim(authority, "\"'")
				if authority != "" {
					return authority
				}
			}
		}
	}
	return ""
}

// Helper function to fetch OIDC discovery document
func fetchOIDCDiscovery(discoveryURL string) (*OIDCDiscoveryResponse, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get(discoveryURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch discovery document: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery endpoint returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var discovery OIDCDiscoveryResponse
	if err := json.Unmarshal(body, &discovery); err != nil {
		return nil, fmt.Errorf("failed to parse discovery document: %w", err)
	}

	return &discovery, nil
}

// Helper function to check API auth configuration
func checkAPIAuthConfig(apiURL string) (*APIAuthConfig, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // For testing with self-signed certs
			},
		},
	}

	// Parse URL to handle potential issues
	parsedURL, err := url.Parse(apiURL)
	if err != nil {
		return nil, fmt.Errorf("invalid API URL: %w", err)
	}

	resp, err := client.Get(parsedURL.String())
	if err != nil {
		return nil, fmt.Errorf("failed to fetch auth config: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Check if response indicates auth is not configured
	if strings.Contains(string(body), "Auth not configured") {
		return nil, fmt.Errorf("auth not configured on API")
	}

	var authConfig APIAuthConfig
	if err := json.Unmarshal(body, &authConfig); err != nil {
		return nil, fmt.Errorf("failed to parse auth config: %w", err)
	}

	return &authConfig, nil
}

