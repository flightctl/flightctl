//go:build linux

package auth_test

import (
	"context"
	"fmt"
	"os/user"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/issuer"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	fccrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/service"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

func TestPAMIssuerServiceIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "PAM Issuer Service Integration Suite")
}

var _ = Describe("PAM Issuer Service Integration Tests", func() {
	var (
		provider       *issuer.PAMOIDCProvider
		caClient       *fccrypto.CAClient
		serviceHandler service.Service
	)

	BeforeEach(func() {
		// Create test CA client
		cfg := ca.NewDefault(GinkgoT().TempDir())
		var err error
		caClient, _, err = fccrypto.EnsureCA(cfg)
		Expect(err).ToNot(HaveOccurred())

		// Create PAM issuer with real components
		pamConfig := &config.PAMOIDCIssuer{
			Issuer:       "https://test.example.com",
			Scopes:       []string{"openid", "profile", "email", "offline_access"},
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURIs: []string{"https://example.com/callback"},
		}

		// Create a mock PAM authenticator that accepts a single test user
		mockAuthenticator := &MockPAMAuthenticator{
			validUser:     "testuser",
			validPassword: "testpass",
		}

		provider, err = issuer.NewPAMOIDCProviderWithAuthenticator(caClient, pamConfig, mockAuthenticator)
		Expect(err).ToNot(HaveOccurred())
		Expect(provider).ToNot(BeNil())

		// Create service handler with the OIDC issuer
		serviceHandler = service.NewServiceHandler(nil, nil, nil, caClient, nil, "", "", []string{}, provider)
	})

	AfterEach(func() {
		if provider != nil {
			provider.Close()
		}
	})

	Context("Complete OIDC Flow with Real Auth Code", func() {
		It("should handle the complete flow with a real authorization code", func() {
			// This test simulates the complete OIDC flow by:
			// 1. Getting authorization (returns login form)
			// 2. Simulating login (would return auth code in real scenario)
			// 3. Using the auth code to get tokens
			// 4. Using tokens to get user info

			By("Step 1: Getting authorization (should return login form)")
			authParams := &v1alpha1.AuthAuthorizeParams{
				ClientId:     "test-client",
				RedirectUri:  "https://example.com/callback",
				ResponseType: v1alpha1.AuthAuthorizeParamsResponseTypeCode,
				State:        lo.ToPtr("test-state"),
				Scope:        lo.ToPtr("openid profile email offline_access"),
			}

			result, status := serviceHandler.AuthAuthorize(context.Background(), *authParams)
			Expect(status.Code).To(Equal(int32(200)))
			Expect(result).ToNot(BeNil())
			Expect(result.Message).To(ContainSubstring("Flightctl Login"))

			By("Step 2: Simulating login with mock PAM authenticator")
			// Use the credentials that our mock authenticator will accept
			loginResult, loginStatus := serviceHandler.AuthLogin(context.Background(), "testuser", "testpass", "test-client", "https://example.com/callback", "test-state")
			fmt.Printf("DEBUG: Login result: %+v, status: %+v\n", loginResult, loginStatus)

			// With our mock authenticator, this should succeed and return a redirect URL with session ID
			Expect(loginStatus.Code).To(Equal(int32(200)))
			Expect(loginResult).ToNot(BeNil())
			fmt.Printf("DEBUG: Login successful, redirect URL: %s\n", loginResult.Message)

			// The login returns a redirect URL with session_id, not the final auth code
			// We need to follow this redirect to get the actual authorization code
			redirectURL := loginResult.Message
			fmt.Printf("DEBUG: Following redirect to get auth code: %s\n", redirectURL)

			// Extract session ID from the redirect URL
			sessionID, err := extractSessionIDFromURL(redirectURL)
			Expect(err).ToNot(HaveOccurred())
			Expect(sessionID).ToNot(BeEmpty())
			fmt.Printf("DEBUG: Extracted session ID: %s\n", sessionID)

			// Now we need to call the authorization endpoint again with the session ID
			// This should return the final redirect URL with the authorization code
			By("Step 3: Following redirect with session to get auth code")
			authParamsWithSession := &v1alpha1.AuthAuthorizeParams{
				ClientId:     "test-client",
				RedirectUri:  "https://example.com/callback",
				ResponseType: v1alpha1.AuthAuthorizeParamsResponseTypeCode,
				State:        lo.ToPtr("test-state"),
				Scope:        lo.ToPtr("openid profile email offline_access"),
			}

			// In a real scenario, the session ID would be passed as a cookie or header
			// For this test, we'll pass the session ID in the context
			ctx := context.WithValue(context.Background(), consts.SessionIDCtxKey, sessionID)
			authResult, authStatus := serviceHandler.AuthAuthorize(ctx, *authParamsWithSession)
			fmt.Printf("DEBUG: Auth with session result: %+v, status: %+v\n", authResult, authStatus)

			// Extract the authorization code from the final redirect URL
			authCode, err := extractAuthCodeFromURL(authResult.Message)
			Expect(err).ToNot(HaveOccurred())
			Expect(authCode).ToNot(BeEmpty())
			fmt.Printf("DEBUG: Extracted real auth code: %s\n", authCode)

			By("Step 4: Using real auth code to get tokens")
			tokenRequest := v1alpha1.TokenRequest{
				GrantType:    "authorization_code",
				Code:         lo.ToPtr(authCode),
				ClientId:     lo.ToPtr("test-client"),
				ClientSecret: lo.ToPtr("test-secret"),
			}

			tokenResult, tokenStatus := serviceHandler.AuthToken(context.Background(), tokenRequest)
			fmt.Printf("DEBUG: Token result: %+v, status: %+v\n", tokenResult, tokenStatus)

			// In a real scenario with valid auth code, this would succeed
			Expect(tokenStatus.Code).To(Equal(int32(200)))
			Expect(tokenResult).ToNot(BeNil())
			Expect(tokenResult.AccessToken).ToNot(BeNil())
			Expect(*tokenResult.AccessToken).ToNot(BeEmpty())
			accessToken := *tokenResult.AccessToken

			By("Step 5: Using access token for user info")
			userInfoResult, userInfoStatus := serviceHandler.AuthUserInfo(context.Background(), accessToken)
			fmt.Printf("DEBUG: UserInfo result: %+v, status: %+v\n", userInfoResult, userInfoStatus)

			// Debug output to see what we actually got
			if userInfoResult != nil {
				fmt.Printf("DEBUG: Roles: %+v\n", userInfoResult.Roles)
				fmt.Printf("DEBUG: Organizations: %+v\n", userInfoResult.Organizations)
			}

			// In a real scenario, this would return user information
			Expect(userInfoStatus.Code).To(Equal(int32(200)))
			Expect(userInfoResult).ToNot(BeNil())
			fmt.Printf("DEBUG: Complete OIDC flow successful!\n")

			// Assert that we got a valid access token
			Expect(tokenResult.AccessToken).ToNot(BeNil())
			Expect(*tokenResult.AccessToken).ToNot(BeEmpty())

			// Assert that we got a valid refresh token
			Expect(tokenResult.RefreshToken).ToNot(BeNil())
			Expect(*tokenResult.RefreshToken).ToNot(BeEmpty())

			// Assert that we got valid user info
			Expect(userInfoResult.Sub).ToNot(BeNil())
			Expect(*userInfoResult.Sub).ToNot(BeEmpty())

			// Assert that the user info contains expected fields
			Expect(userInfoResult.PreferredUsername).ToNot(BeNil())
			Expect(*userInfoResult.PreferredUsername).To(Equal("testuser"))

			// Assert that we got roles and organizations from the mock authenticator
			Expect(userInfoResult.Roles).ToNot(BeNil())
			// The system should now return the actual groups from NSS as roles
			Expect(*userInfoResult.Roles).To(ContainElement("users"))
			Expect(*userInfoResult.Roles).To(ContainElement("testgroup"))

			Expect(userInfoResult.Organizations).ToNot(BeNil())
			Expect(*userInfoResult.Organizations).ToNot(BeEmpty())
			// Verify that we got the organization groups (org:engineering, org:devops)
			Expect(*userInfoResult.Organizations).To(ContainElement("engineering"))
			Expect(*userInfoResult.Organizations).To(ContainElement("devops"))

		})
	})

	Context("Service Error Handling", func() {
		It("should handle malformed authorization requests", func() {
			By("Testing missing client_id")
			authParams := &v1alpha1.AuthAuthorizeParams{
				ClientId:     "", // Missing client ID
				RedirectUri:  "https://example.com/callback",
				ResponseType: v1alpha1.AuthAuthorizeParamsResponseTypeCode,
				State:        lo.ToPtr("test-state"),
				Scope:        lo.ToPtr("openid profile email offline_access"),
			}

			fmt.Printf("DEBUG: Testing malformed authorization with params: %+v\n", authParams)
			_, status := serviceHandler.AuthAuthorize(context.Background(), *authParams)
			if status.Code != 200 {
				fmt.Printf("DEBUG: Authorization error (expected): %v\n", status)
			}
			// Should return error for missing client_id
			Expect(status.Code).To(Equal(int32(400)))
			Expect(status.Message).To(ContainSubstring("invalid_request"))
		})

		It("should handle unsupported response types", func() {
			By("Testing unsupported response_type")
			authParams := &v1alpha1.AuthAuthorizeParams{
				ClientId:     "test-client",
				RedirectUri:  "https://example.com/callback",
				ResponseType: "token", // Unsupported response type
				State:        lo.ToPtr("test-state"),
				Scope:        lo.ToPtr("openid profile email offline_access"),
			}

			fmt.Printf("DEBUG: Testing unsupported response type with params: %+v\n", authParams)
			_, status := serviceHandler.AuthAuthorize(context.Background(), *authParams)
			if status.Code != 200 {
				fmt.Printf("DEBUG: Authorization error (expected): %v\n", status)
			}
			// Should return error for unsupported response type
			Expect(status.Code).To(Equal(int32(400)))
			Expect(status.Message).To(ContainSubstring("unsupported_response_type"))
		})

		It("should handle invalid client credentials", func() {
			By("Testing invalid client_id")
			authParams := &v1alpha1.AuthAuthorizeParams{
				ClientId:     "invalid-client", // Wrong client ID
				RedirectUri:  "https://example.com/callback",
				ResponseType: v1alpha1.AuthAuthorizeParamsResponseTypeCode,
				State:        lo.ToPtr("test-state"),
				Scope:        lo.ToPtr("openid profile email offline_access"),
			}

			fmt.Printf("DEBUG: Testing invalid client with params: %+v\n", authParams)
			_, status := serviceHandler.AuthAuthorize(context.Background(), *authParams)
			if status.Code != 200 {
				fmt.Printf("DEBUG: Authorization error (expected): %v\n", status)
			}
			// Should return error for invalid client
			Expect(status.Code).To(Equal(int32(400)))
			Expect(status.Message).To(ContainSubstring("invalid_client"))
		})
	})
})

// MockPAMAuthenticator is a mock implementation of PAMAuthenticator for testing
type MockPAMAuthenticator struct {
	validUser     string
	validPassword string
}

// Authenticate implements the PAMAuthenticator interface
func (m *MockPAMAuthenticator) Authenticate(username, password string) error {
	if username == m.validUser && password == m.validPassword {
		return nil
	}
	return fmt.Errorf("authentication failed for user %s", username)
}

// LookupUser implements the PAMAuthenticator interface
func (m *MockPAMAuthenticator) LookupUser(username string) (*user.User, error) {
	if username == m.validUser {
		return &user.User{
			Uid:      "1000",
			Gid:      "1000",
			Username: username,
			Name:     "Test User",
			HomeDir:  "/home/" + username,
		}, nil
	}
	return nil, fmt.Errorf("user %s not found", username)
}

// GetUserGroups implements the PAMAuthenticator interface
func (m *MockPAMAuthenticator) GetUserGroups(systemUser *user.User) ([]string, error) {
	if systemUser.Username == m.validUser {
		return []string{"users", "testgroup", "org:engineering", "org:devops"}, nil
	}
	return nil, fmt.Errorf("user %s not found", systemUser.Username)
}

// Close implements the PAMAuthenticator interface
func (m *MockPAMAuthenticator) Close() error {
	return nil
}

// extractAuthCodeFromURL extracts the authorization code from a redirect URL
func extractAuthCodeFromURL(url string) (string, error) {
	// Simple URL parsing to extract the 'code' parameter
	// In a real scenario, you'd use url.Parse and url.Query()
	if strings.Contains(url, "code=") {
		parts := strings.Split(url, "code=")
		if len(parts) > 1 {
			codePart := parts[1]
			// Remove any additional parameters after the code
			if ampIndex := strings.Index(codePart, "&"); ampIndex != -1 {
				return codePart[:ampIndex], nil
			}
			return codePart, nil
		}
	}
	return "", fmt.Errorf("no authorization code found in URL")
}

// extractSessionIDFromURL extracts the session ID from a redirect URL
func extractSessionIDFromURL(url string) (string, error) {
	// Simple URL parsing to extract the 'session_id' parameter
	if strings.Contains(url, "session_id=") {
		parts := strings.Split(url, "session_id=")
		if len(parts) > 1 {
			sessionPart := parts[1]
			// Remove any additional parameters after the session ID
			if ampIndex := strings.Index(sessionPart, "&"); ampIndex != -1 {
				return sessionPart[:ampIndex], nil
			}
			return sessionPart, nil
		}
	}
	return "", fmt.Errorf("no session ID found in URL")
}
