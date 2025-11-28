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

	var (
		keycloakIP   = "localhost"
		keycloakPort = "8080"
		realmName    = "myrealm"
		clientID     = "my_client"
		testUser     = "testuser"
		testPassword = "password"
	)

	It("should configure and validate OIDC authentication end-to-end", func() {
		// Step 1: Deploy Keycloak container
		By("deploying Keycloak container")
		deployKeycloak(keycloakPort)

		// Step 2: Wait for Keycloak to be ready
		By("waiting for Keycloak to be ready")
		Eventually(func() bool {
			return isKeycloakReady(keycloakIP, keycloakPort)
		}, 120*time.Second, 5*time.Second).Should(BeTrue(), "Keycloak should be ready")
		GinkgoWriter.Printf("✓ Keycloak is ready\n")

		// Step 3: Get admin token
		By("authenticating with Keycloak admin")
		adminToken, err := getKeycloakAdminToken(keycloakIP, keycloakPort)
		Expect(err).ToNot(HaveOccurred())
		Expect(adminToken).ToNot(BeEmpty())
		GinkgoWriter.Printf("✓ Admin token obtained\n")

		// Step 4: Create realm
		By(fmt.Sprintf("creating OIDC realm '%s'", realmName))
		err = createKeycloakRealm(keycloakIP, keycloakPort, adminToken, realmName)
		Expect(err).ToNot(HaveOccurred())
		GinkgoWriter.Printf("✓ Realm '%s' created\n", realmName)

		// Step 5: Create client
		By(fmt.Sprintf("creating OIDC client '%s'", clientID))
		err = createKeycloakClient(keycloakIP, keycloakPort, adminToken, realmName, clientID)
		Expect(err).ToNot(HaveOccurred())
		GinkgoWriter.Printf("✓ Client '%s' created\n", clientID)

		// Step 6: Create test user
		By(fmt.Sprintf("creating test user '%s'", testUser))
		err = createKeycloakUser(keycloakIP, keycloakPort, adminToken, realmName, testUser, testPassword)
		Expect(err).ToNot(HaveOccurred())
		GinkgoWriter.Printf("✓ User '%s' created\n", testUser)

		// Step 7: Verify OIDC discovery
		By("verifying OIDC discovery endpoint")
		discoveryURL := fmt.Sprintf("http://%s:%s/realms/%s/.well-known/openid-configuration",
			keycloakIP, keycloakPort, realmName)
		discoveryData, err := fetchOIDCDiscovery(discoveryURL)
		Expect(err).ToNot(HaveOccurred())
		Expect(discoveryData.Issuer).ToNot(BeEmpty())
		GinkgoWriter.Printf("✓ OIDC Discovery successful\n")
		GinkgoWriter.Printf("  Issuer: %s\n", discoveryData.Issuer)
		GinkgoWriter.Printf("  Authorization Endpoint: %s\n", discoveryData.AuthorizationEndpoint)
		GinkgoWriter.Printf("  Token Endpoint: %s\n", discoveryData.TokenEndpoint)

		// Step 8: Test user authentication
		By("testing user authentication with Keycloak")
		accessToken, err := authenticateUser(keycloakIP, keycloakPort, realmName, clientID, testUser, testPassword)
		Expect(err).ToNot(HaveOccurred())
		Expect(accessToken).ToNot(BeEmpty())
		GinkgoWriter.Printf("✓ User authentication successful\n")
		GinkgoWriter.Printf("  Access token obtained (length: %d)\n", len(accessToken))

		// Step 9: Configure FlightCtl OIDC settings
		By("configuring FlightCtl OIDC settings")
		oidcAuthority := fmt.Sprintf("http://%s:%s/realms/%s", keycloakIP, keycloakPort, realmName)
		err = configureFlightCtlOIDC(oidcAuthority, clientID)
		Expect(err).ToNot(HaveOccurred())
		GinkgoWriter.Printf("✓ FlightCtl OIDC configuration updated\n")

		// Step 10: Restart FlightCtl services
		By("restarting FlightCtl services to apply OIDC configuration")
		err = restartFlightCtlServices()
		Expect(err).ToNot(HaveOccurred())
		GinkgoWriter.Printf("✓ FlightCtl services restarted\n")

		// Step 11: Wait for services to stabilize
		By("waiting for FlightCtl services to stabilize")
		time.Sleep(30 * time.Second)

		// Step 12: Verify API auth configuration
		By("verifying FlightCtl API auth configuration")
		apiURL := "https://localhost:3443/api/v1/auth/config"
		Eventually(func() error {
			authConfig, err := checkAPIAuthConfig(apiURL)
			if err != nil {
				return err
			}
			if authConfig.Type != "oidc" {
				return fmt.Errorf("expected auth type 'oidc', got '%s'", authConfig.Type)
			}
			GinkgoWriter.Printf("✓ API reports OIDC authentication enabled\n")
			GinkgoWriter.Printf("  Auth Type: %s\n", authConfig.Type)
			GinkgoWriter.Printf("  OIDC Client ID: %s\n", authConfig.OIDCClientID)
			GinkgoWriter.Printf("  OIDC Authority: %s\n", authConfig.OIDCAuthority)
			return nil
		}, 120*time.Second, 10*time.Second).Should(Succeed(), "API should report OIDC is configured")

		// Step 13: Verify PAM OIDC Issuer service
		By("checking PAM OIDC Issuer service status")
		pamServiceExists, err := util.SystemdUnitExists("flightctl-pam-issuer.service")
		if err == nil && pamServiceExists {
			GinkgoWriter.Printf("✓ PAM OIDC Issuer service exists\n")

			// Check service status
			Eventually(func() bool {
				status, _ := util.GetSystemdStatus("flightctl-pam-issuer.service")
				return strings.Contains(status, "active (running)")
			}, 60*time.Second, 5*time.Second).Should(BeTrue(), "PAM OIDC Issuer should be running")

			GinkgoWriter.Printf("✓ PAM OIDC Issuer service is running\n")
		} else {
			GinkgoWriter.Printf("⚠ PAM OIDC Issuer service not found (may not be required)\n")
		}

		// Step 14: Summary
		By("OIDC authentication configuration complete")
		GinkgoWriter.Printf("\n")
		GinkgoWriter.Printf("========================================\n")
		GinkgoWriter.Printf("OIDC Configuration Summary\n")
		GinkgoWriter.Printf("========================================\n")
		GinkgoWriter.Printf("Keycloak Admin:    http://%s:%s/admin\n", keycloakIP, keycloakPort)
		GinkgoWriter.Printf("OIDC Realm:        %s\n", realmName)
		GinkgoWriter.Printf("OIDC Client:       %s\n", clientID)
		GinkgoWriter.Printf("Test User:         %s / %s\n", testUser, testPassword)
		GinkgoWriter.Printf("OIDC Authority:    %s\n", oidcAuthority)
		GinkgoWriter.Printf("========================================\n")
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
	Type          string `json:"type"`
	OIDCClientID  string `json:"oidcClientId,omitempty"`
	OIDCAuthority string `json:"oidcAuthority,omitempty"`
}

// KeycloakTokenResponse represents the token response from Keycloak
type KeycloakTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// deployKeycloak deploys Keycloak container
func deployKeycloak(port string) {
	GinkgoWriter.Printf("Deploying Keycloak container on port %s...\n", port)

	// Check if Keycloak is already running
	_, err := util.ExecCommand("sudo", "podman", "ps", "--filter", "name=^keycloak$", "--format", "{{.Names}}")
	if err == nil {
		GinkgoWriter.Printf("✓ Keycloak container already exists\n")
		// Try to start it if it's stopped
		_, _ = util.ExecCommand("sudo", "podman", "start", "keycloak")
		return
	}

	// Deploy new Keycloak container
	cmd := fmt.Sprintf(`sudo podman run -d --name keycloak \
		--restart always \
		-p %s:8080 \
		-p 9000:9000 \
		-e KEYCLOAK_ADMIN=admin \
		-e KEYCLOAK_ADMIN_PASSWORD=admin \
		-e KC_HEALTH_ENABLED=true \
		quay.io/keycloak/keycloak:latest \
		start-dev`, port)

	_, err = util.ExecCommand("sh", "-c", cmd)
	Expect(err).ToNot(HaveOccurred(), "Failed to deploy Keycloak")
	GinkgoWriter.Printf("✓ Keycloak container deployed\n")
}

// isKeycloakReady checks if Keycloak is ready
func isKeycloakReady(host, port string) bool {
	healthURL := fmt.Sprintf("http://%s:9000/health/ready", host)
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(healthURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	body, _ := io.ReadAll(resp.Body)
	return strings.Contains(string(body), `"status": "UP"`)
}

// getKeycloakAdminToken gets admin access token
func getKeycloakAdminToken(host, port string) (string, error) {
	tokenURL := fmt.Sprintf("http://%s:%s/realms/master/protocol/openid-connect/token", host, port)

	data := url.Values{}
	data.Set("username", "admin")
	data.Set("password", "admin")
	data.Set("grant_type", "password")
	data.Set("client_id", "admin-cli")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.PostForm(tokenURL, data)
	if err != nil {
		return "", fmt.Errorf("failed to get admin token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get admin token, status: %d", resp.StatusCode)
	}

	var tokenResp KeycloakTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}

	return tokenResp.AccessToken, nil
}

// createKeycloakRealm creates a new realm
func createKeycloakRealm(host, port, adminToken, realmName string) error {
	realmURL := fmt.Sprintf("http://%s:%s/admin/realms", host, port)

	realmData := fmt.Sprintf(`{
		"realm": "%s",
		"enabled": true,
		"sslRequired": "none",
		"registrationAllowed": false,
		"loginWithEmailAllowed": true,
		"duplicateEmailsAllowed": false
	}`, realmName)

	req, err := http.NewRequest("POST", realmURL, strings.NewReader(realmData))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 201 Created or 409 Conflict (already exists) are both OK
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to create realm, status: %d, body: %s", resp.StatusCode, string(body))
	}

	return nil
}

// createKeycloakClient creates OIDC client
func createKeycloakClient(host, port, adminToken, realmName, clientID string) error {
	clientURL := fmt.Sprintf("http://%s:%s/admin/realms/%s/clients", host, port, realmName)

	clientData := fmt.Sprintf(`{
		"clientId": "%s",
		"enabled": true,
		"publicClient": true,
		"redirectUris": ["https://localhost:443/*", "http://127.0.0.1/*", "urn:ietf:wg:oauth:2.0:oob"],
		"webOrigins": ["*"],
		"directAccessGrantsEnabled": true,
		"standardFlowEnabled": true,
		"protocol": "openid-connect"
	}`, clientID)

	req, err := http.NewRequest("POST", clientURL, strings.NewReader(clientData))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to create client, status: %d, body: %s", resp.StatusCode, string(body))
	}

	return nil
}

// createKeycloakUser creates a test user
func createKeycloakUser(host, port, adminToken, realmName, username, password string) error {
	usersURL := fmt.Sprintf("http://%s:%s/admin/realms/%s/users", host, port, realmName)

	userData := fmt.Sprintf(`{
		"username": "%s",
		"enabled": true,
		"email": "%s@example.com",
		"emailVerified": true
	}`, username, username)

	req, err := http.NewRequest("POST", usersURL, strings.NewReader(userData))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to create user, status: %d, body: %s", resp.StatusCode, string(body))
	}

	// Set user password
	getUserURL := fmt.Sprintf("http://%s:%s/admin/realms/%s/users?username=%s", host, port, realmName, username)
	req, _ = http.NewRequest("GET", getUserURL, nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)

	resp, err = client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var users []map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&users)

	if len(users) == 0 {
		return fmt.Errorf("user not found after creation")
	}

	userID := users[0]["id"].(string)
	passwordURL := fmt.Sprintf("http://%s:%s/admin/realms/%s/users/%s/reset-password", host, port, realmName, userID)
	passwordData := fmt.Sprintf(`{"type":"password","value":"%s","temporary":false}`, password)

	req, _ = http.NewRequest("PUT", passwordURL, strings.NewReader(passwordData))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err = client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}

// fetchOIDCDiscovery fetches OIDC discovery document
func fetchOIDCDiscovery(discoveryURL string) (*OIDCDiscoveryResponse, error) {
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(discoveryURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch discovery document: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery endpoint returned status %d", resp.StatusCode)
	}

	var discovery OIDCDiscoveryResponse
	if err := json.NewDecoder(resp.Body).Decode(&discovery); err != nil {
		return nil, fmt.Errorf("failed to parse discovery document: %w", err)
	}

	return &discovery, nil
}

// authenticateUser tests user authentication
func authenticateUser(host, port, realmName, clientID, username, password string) (string, error) {
	tokenURL := fmt.Sprintf("http://%s:%s/realms/%s/protocol/openid-connect/token", host, port, realmName)

	data := url.Values{}
	data.Set("username", username)
	data.Set("password", password)
	data.Set("grant_type", "password")
	data.Set("client_id", clientID)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.PostForm(tokenURL, data)
	if err != nil {
		return "", fmt.Errorf("failed to authenticate user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("authentication failed, status: %d", resp.StatusCode)
	}

	var tokenResp KeycloakTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}

	return tokenResp.AccessToken, nil
}

// configureFlightCtlOIDC updates FlightCtl configuration for OIDC
func configureFlightCtlOIDC(oidcAuthority, clientID string) error {
	GinkgoWriter.Printf("Configuring FlightCtl OIDC...\n")

	// Update service-config.yaml
	commands := []string{
		`sudo sed -i 's/type: none/type: oidc/' /etc/flightctl/service-config.yaml || true`,
		fmt.Sprintf(`sudo sed -i 's|oidcAuthority:.*|oidcAuthority: "%s"|' /etc/flightctl/service-config.yaml || true`, oidcAuthority),
		fmt.Sprintf(`sudo sed -i 's|externalOidcAuthority:.*|externalOidcAuthority: "%s"|' /etc/flightctl/service-config.yaml || true`, oidcAuthority),
		fmt.Sprintf(`sudo sed -i 's|oidcClientId:.*|oidcClientId: "%s"|' /etc/flightctl/service-config.yaml || true`, clientID),
	}

	for _, cmd := range commands {
		if _, err := util.ExecCommand("sh", "-c", cmd); err != nil {
			GinkgoWriter.Printf("⚠ Command failed (may be acceptable): %s\n", cmd)
		}
	}

	// Regenerate API config
	util.ExecCommand("sudo", "rm", "-f", "/etc/flightctl/flightctl-api/config.yaml")
	util.ExecCommand("sudo", "systemctl", "unmask", "flightctl-api-init.service")
	util.ExecCommand("sudo", "systemctl", "restart", "flightctl-api-init.service")

	// Wait for config generation
	time.Sleep(5 * time.Second)

	return nil
}

// restartFlightCtlServices restarts FlightCtl services
func restartFlightCtlServices() error {
	GinkgoWriter.Printf("Restarting FlightCtl services...\n")

	_, err := util.ExecCommand("sudo", "systemctl", "restart", "flightctl.target")
	return err
}

// checkAPIAuthConfig checks API auth configuration
func checkAPIAuthConfig(apiURL string) (*APIAuthConfig, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	resp, err := client.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch auth config: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if strings.Contains(string(body), "Auth not configured") {
		return nil, fmt.Errorf("auth not configured on API")
	}

	var authConfig APIAuthConfig
	if err := json.NewDecoder(strings.NewReader(string(body))).Decode(&authConfig); err != nil {
		return nil, fmt.Errorf("failed to parse auth config: %w", err)
	}

	return &authConfig, nil
}
