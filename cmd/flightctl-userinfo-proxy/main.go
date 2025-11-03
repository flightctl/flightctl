package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/shutdown"
	"github.com/sirupsen/logrus"
)

// Config holds the configuration for the userinfo proxy
type Config struct {
	ListenPort          string
	UpstreamURL         string
	SkipTLSVerification bool
}

// UserInfoResponse represents the OpenID Connect UserInfo response
type UserInfoResponse struct {
	Sub               string   `json:"sub"`
	Name              string   `json:"name,omitempty"`
	GivenName         string   `json:"given_name,omitempty"`
	FamilyName        string   `json:"family_name,omitempty"`
	PreferredUsername string   `json:"preferred_username,omitempty"`
	Email             string   `json:"email,omitempty"`
	EmailVerified     bool     `json:"email_verified,omitempty"`
	Picture           string   `json:"picture,omitempty"`
	Groups            []string `json:"groups,omitempty"`
	Roles             []string `json:"roles,omitempty"`
	UpdatedAt         int64    `json:"updated_at,omitempty"`
}

func main() {
	ctx := context.Background()

	if err := runCmd(ctx); err != nil {
		log.InitLogs().Fatalf("UserInfo proxy error: %v", err)
	}
}

func runCmd(ctx context.Context) error {
	logger := log.InitLogs()

	logger.Info("Starting UserInfo proxy server")
	defer logger.Info("UserInfo proxy server stopped")

	config := loadConfig(logger)

	http.HandleFunc("/userinfo", makeUserInfoHandler(config, logger))

	logger.Infof("Starting UserInfo proxy server on port %s", config.ListenPort)
	logger.Infof("Proxying to upstream: %s", config.UpstreamURL)
	logger.Infof("TLS verification: %s", map[bool]string{true: "enabled", false: "disabled"}[!config.SkipTLSVerification])

	// Create server with proper timeouts to prevent DoS attacks
	server := &http.Server{
		Addr:              ":" + config.ListenPort,
		ReadHeaderTimeout: 10 * time.Second, // Time to read request headers
		ReadTimeout:       30 * time.Second, // Time to read entire request
		WriteTimeout:      30 * time.Second, // Time to write response
		IdleTimeout:       60 * time.Second, // Time to keep idle connections open
	}

	// Channel to coordinate shutdown completion
	shutdownComplete := make(chan struct{})

	// Set up graceful shutdown
	shutdown.GracefulShutdown(logger, shutdownComplete, func(shutdownCtx context.Context) error {
		// Shutdown server first
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.WithError(err).Error("Server shutdown error")
			return err
		}

		return nil
	})

	// Start server in background
	serverDone := make(chan error, 1)
	go func() {
		logger.Printf("UserInfo proxy server starting on %s...", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.WithError(err).Error("Server error")
			serverDone <- err
		} else {
			serverDone <- nil
		}
	}()

	logger.Info("UserInfo proxy server started, waiting for shutdown signal...")

	// Wait for either server error or shutdown signal
	select {
	case err := <-serverDone:
		if err != nil {
			logger.WithError(err).Error("Server failed")
			return err // Return error for non-zero exit code
		}
	case <-shutdownComplete:
		// Graceful shutdown completed
	}

	logger.Info("Server stopped, exiting...")
	return nil
}

func loadConfig(log *logrus.Logger) *Config {
	config := &Config{
		ListenPort:          getEnv("USERINFO_LISTEN_PORT", "8080"),
		UpstreamURL:         getEnv("USERINFO_UPSTREAM_URL", ""),
		SkipTLSVerification: getBoolEnv("USERINFO_SKIP_TLS_VERIFY", false, log),
	}

	if config.UpstreamURL == "" {
		log.Fatal("USERINFO_UPSTREAM_URL environment variable is required")
	}

	return config
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getBoolEnv(key string, defaultValue bool, log *logrus.Logger) bool {
	if value := os.Getenv(key); value != "" {
		switch strings.ToLower(value) {
		case "true", "1", "yes", "on":
			return true
		case "false", "0", "no", "off":
			return false
		default:
			log.Warnf("Invalid boolean value for %s: %s, using default: %v", key, value, defaultValue)
			return defaultValue
		}
	}
	return defaultValue
}

// AAPResponse represents the response from Ansible Automation Platform user API
type AAPResponse struct {
	Count    int       `json:"count"`
	Next     *string   `json:"next"`
	Previous *string   `json:"previous"`
	Results  []AAPUser `json:"results"`
}

// AAPUser represents a user from Ansible Automation Platform
type AAPUser struct {
	ID          int    `json:"id"`
	Username    string `json:"username"`
	Email       string `json:"email"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	IsSuperuser bool   `json:"is_superuser"`
	IsAuditor   bool   `json:"is_platform_auditor"`
}

func makeUserInfoHandler(config *Config, log *logrus.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only allow GET requests
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Extract Authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Authorization header required", http.StatusUnauthorized)
			return
		}

		// Proxy request to upstream AAP
		aapResp, err := proxyToUpstream(config, authHeader, log)
		if err != nil {
			log.WithError(err).Error("Error proxying to upstream AAP")
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		// Transform AAP response to UserInfo format
		userInfo, err := transformToUserInfo(aapResp)
		if err != nil {
			log.WithError(err).Error("Error transforming response")
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		// Return JSON response
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(userInfo); err != nil {
			log.WithError(err).Error("Error encoding response")
		}
	}
}

func proxyToUpstream(config *Config, authHeader string, log *logrus.Logger) (*AAPResponse, error) {
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: config.SkipTLSVerification}, // #nosec G402
		},

		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequest("GET", config.UpstreamURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("making request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upstream returned status %d: %s", resp.StatusCode, string(body))
	}

	var result AAPResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding JSON: %w", err)
	}

	return &result, nil
}

func transformToUserInfo(aapResp *AAPResponse) (*UserInfoResponse, error) {
	if len(aapResp.Results) == 0 {
		return nil, fmt.Errorf("no user found in IdP response")
	}

	// Take the first user from results
	user := aapResp.Results[0]

	userInfo := &UserInfoResponse{
		Sub:               fmt.Sprintf("%d", user.ID),
		Email:             user.Email,
		EmailVerified:     true, // Assume verified since it's from IdP
		PreferredUsername: user.Username,
		UpdatedAt:         time.Now().Unix(),
	}

	// Build full name from first and last name
	if user.FirstName != "" || user.LastName != "" {
		userInfo.Name = strings.TrimSpace(user.FirstName + " " + user.LastName)
		userInfo.GivenName = user.FirstName
		userInfo.FamilyName = user.LastName
	} else {
		userInfo.Name = user.Username
		userInfo.GivenName = user.Username
	}

	// Determine Grafana role based on IdP permissions
	if user.IsSuperuser {
		userInfo.Roles = []string{"Admin"}
		userInfo.Groups = []string{"admin"}
	} else if user.IsAuditor {
		userInfo.Roles = []string{"Editor"}
		userInfo.Groups = []string{"auditor"}
	} else {
		userInfo.Roles = []string{"Viewer"}
		userInfo.Groups = []string{"user"}
	}

	return userInfo, nil
}
