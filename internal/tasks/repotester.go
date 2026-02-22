package tasks

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const RepoTesterTaskName = "repository-tester"

type API interface {
	Test()
}

// RepoTesterMapping maps a repository to its appropriate TypeSpecificRepoTester.
// Returns nil if the repository type is unsupported.
type RepoTesterMapping func(repository *domain.Repository) TypeSpecificRepoTester

type RepoTester struct {
	log              logrus.FieldLogger
	serviceHandler   service.Service
	RepoTesterMapper RepoTesterMapping
}

func NewRepoTester(log logrus.FieldLogger, serviceHandler service.Service, repoTesterMapper RepoTesterMapping) *RepoTester {
	if repoTesterMapper == nil {
		repoTesterMapper = DefaultRepoTesterMapping
	}
	return &RepoTester{
		log:              log,
		serviceHandler:   serviceHandler,
		RepoTesterMapper: repoTesterMapper,
	}
}

// Cached tester instances (stateless, safe to reuse)
var (
	ociRepoTester  = &OciRepoTester{}
	httpRepoTester = &HttpRepoTester{}
	gitRepoTester  = &GitRepoTester{}
)

// DefaultRepoTesterMapping returns the appropriate TypeSpecificRepoTester based on the repository spec.
// Returns nil if the repository type is unsupported.
func DefaultRepoTesterMapping(repository *domain.Repository) TypeSpecificRepoTester {
	specType, err := repository.Spec.Discriminator()
	if err != nil {
		return nil
	}
	switch domain.RepoSpecType(specType) {
	case domain.RepoSpecTypeOci:
		return ociRepoTester
	case domain.RepoSpecTypeHttp:
		return httpRepoTester
	case domain.RepoSpecTypeGit:
		return gitRepoTester
	default:
		return nil
	}
}

func (r *RepoTester) TestRepositories(ctx context.Context, orgId uuid.UUID) {
	log := log.WithReqIDFromCtx(ctx, r.log)

	log.Info("Running RepoTester")

	limit := int32(ItemsPerPage)
	continueToken := (*string)(nil)

	for {
		repositories, status := r.serviceHandler.ListRepositories(ctx, orgId, domain.ListRepositoriesParams{
			Limit:    &limit,
			Continue: continueToken,
		})
		if status.Code != 200 {
			log.Errorf("error fetching repositories: %s", status.Message)
			return
		}

		for i := range repositories.Items {
			repository := repositories.Items[i]

			tester := r.RepoTesterMapper(&repository)
			if tester == nil {
				repoType, _ := repository.Spec.Discriminator()
				log.Infof("Skipping unsupported repository type: %s", repoType)
				continue
			}

			r.testRepository(ctx, orgId, repository, tester)
		}

		continueToken = repositories.Metadata.Continue
		if continueToken == nil {
			break
		}
	}
}

func (r *RepoTester) SetAccessCondition(ctx context.Context, orgId uuid.UUID, repository *domain.Repository, err error) error {
	if repository.Status == nil {
		repository.Status = &domain.RepositoryStatus{Conditions: []domain.Condition{}}
	}
	if repository.Status.Conditions == nil {
		repository.Status.Conditions = []domain.Condition{}
	}
	_, status := r.serviceHandler.ReplaceRepositoryStatusByError(ctx, orgId, lo.FromPtr(repository.Metadata.Name), *repository, err)

	return service.ApiStatusToErr(status)
}

func (r *RepoTester) testRepository(ctx context.Context, orgId uuid.UUID, repository domain.Repository, tester TypeSpecificRepoTester) {
	repoName := *repository.Metadata.Name
	accessErr := tester.TestAccess(&repository)
	if err := r.SetAccessCondition(ctx, orgId, &repository, accessErr); err != nil {
		r.log.Errorf("Failed to update repository status for %s: %v", repoName, err)
	}
}

type TypeSpecificRepoTester interface {
	TestAccess(repository *domain.Repository) error
}

type GitRepoTester struct {
}

type HttpRepoTester struct {
}

type OciRepoTester struct {
}

func (r *GitRepoTester) TestAccess(repository *domain.Repository) error {
	repoURL, err := repository.Spec.GetRepoURL()
	if err != nil {
		return err
	}
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name:  *repository.Metadata.Name,
		URLs:  []string{repoURL},
		Fetch: []config.RefSpec{"HEAD"},
	})

	listOps := &git.ListOptions{}
	auth, err := GetAuth(repository, nil) // nil config for test
	if err != nil {
		return err
	}

	listOps.Auth = auth
	_, err = remote.List(listOps)
	if err != nil {
		// Extract the root cause from go-git wrapped errors
		// Format is often "authentication required: <actual error>"
		errMsg := err.Error()
		if idx := strings.LastIndex(errMsg, ": "); idx != -1 && idx+2 < len(errMsg) {
			// Extract the part after the colon (the actual error)
			errMsg = strings.TrimSpace(errMsg[idx+2:])
		}
		// Remove trailing period if present
		errMsg = strings.TrimSuffix(errMsg, ".")
		return fmt.Errorf("%s", errMsg)
	}
	return nil
}

func (r *HttpRepoTester) TestAccess(repository *domain.Repository) error {
	repoHttpSpec, err := repository.Spec.AsHttpRepoSpec()
	if err != nil {
		return fmt.Errorf("failed to get HTTP repo spec: %w", err)
	}

	repoURL := repoHttpSpec.Url
	// Append the validationSuffix if it exists
	if repoHttpSpec.ValidationSuffix != nil {
		repoURL += *repoHttpSpec.ValidationSuffix
	}

	repoSpec := repository.Spec
	_, err = sendHTTPrequest(repoSpec, repoURL)
	return err
}

func (r *OciRepoTester) TestAccess(repository *domain.Repository) error {
	ociSpec, err := repository.Spec.AsOciRepoSpec()
	if err != nil {
		return fmt.Errorf("failed to get OCI repo spec: %w", err)
	}

	// Build the OCI registry v2 API URL from hostname/FQDN
	scheme := "https"
	if ociSpec.Scheme != nil {
		scheme = string(*ociSpec.Scheme)
	}
	baseURL := &url.URL{
		Scheme: scheme,
		Host:   ociSpec.Registry,
	}
	v2URL := baseURL.JoinPath("/v2/").String()

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if ociSpec.SkipServerVerification != nil {
		tlsConfig.InsecureSkipVerify = *ociSpec.SkipServerVerification
	}
	if ociSpec.CaCrt != nil {
		ca, err := base64.StdEncoding.DecodeString(*ociSpec.CaCrt)
		if err != nil {
			return fmt.Errorf("failed to decode CA certificate: %w", err)
		}
		rootCAs, err := x509.SystemCertPool()
		if err != nil {
			return fmt.Errorf("failed to get system cert pool: %w", err)
		}
		if rootCAs == nil {
			rootCAs = x509.NewCertPool()
		}
		rootCAs.AppendCertsFromPEM(ca)
		tlsConfig.RootCAs = rootCAs
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	// Step 1: Call /v2/ without auth (OCI Distribution Spec)
	resp, err := client.Get(v2URL)
	if err != nil {
		return fmt.Errorf("failed to connect to registry: %w", err)
	}
	defer resp.Body.Close()

	// If 200, registry is accessible without auth
	if resp.StatusCode == http.StatusOK {
		return nil
	}

	// Step 2: Authenticate based on auth type
	if ociSpec.OciAuth != nil {
		authType, err := ociSpec.OciAuth.Discriminator()
		if err != nil {
			return fmt.Errorf("failed to determine OCI auth type: %w", err)
		}
		switch domain.OciAuthType(authType) {
		case domain.Docker:
			return r.authenticateDocker(client, v2URL, resp, ociSpec.OciAuth)
		default:
			return fmt.Errorf("unsupported OCI auth type: %s", authType)
		}
	}

	// No auth configured - try anonymous access with Docker protocol
	return r.authenticateDocker(client, v2URL, resp, nil)
}

// authenticateDocker performs Docker registry token-based authentication (Bearer token exchange)
func (r *OciRepoTester) authenticateDocker(client *http.Client, v2URL string, initialResp *http.Response, ociAuth *domain.OciAuth) error {
	// Docker registries return 401 with Www-Authenticate header for token exchange
	if initialResp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("unexpected status code: %d", initialResp.StatusCode)
	}

	// Parse www-authenticate header to get realm and service
	wwwAuth := initialResp.Header.Get("Www-Authenticate")
	realm, service, err := parseWwwAuthenticate(wwwAuth)
	if err != nil {
		return fmt.Errorf("failed to parse www-authenticate header: %w", err)
	}

	// Get credentials if provided
	var username, password *string
	if ociAuth != nil {
		dockerAuth, err := ociAuth.AsDockerAuth()
		if err != nil {
			return fmt.Errorf("failed to parse docker auth config: %w", err)
		}
		username = &dockerAuth.Username
		password = &dockerAuth.Password
	}

	// Get token from auth endpoint
	token, err := r.getToken(client, realm, service, username, password)
	if err != nil {
		return err
	}

	// Call /v2/ with bearer token
	req, err := http.NewRequest("GET", v2URL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to registry with token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	return fmt.Errorf("authentication failed: status %d", resp.StatusCode)
}

// parseWwwAuthenticate parses the Www-Authenticate header to extract realm and service
// Example: Bearer realm="https://quay.io/v2/auth",service="quay.io"
func parseWwwAuthenticate(header string) (realm, service string, err error) {
	if !strings.HasPrefix(header, "Bearer ") {
		return "", "", fmt.Errorf("unsupported auth type: %s", header)
	}

	params := strings.TrimPrefix(header, "Bearer ")
	for _, part := range strings.Split(params, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "realm=") {
			realm = strings.Trim(strings.TrimPrefix(part, "realm="), "\"")
		} else if strings.HasPrefix(part, "service=") {
			service = strings.Trim(strings.TrimPrefix(part, "service="), "\"")
		}
	}

	if realm == "" {
		return "", "", fmt.Errorf("realm not found in www-authenticate header")
	}

	return realm, service, nil
}

// getToken gets an auth token from the registry's auth endpoint
func (r *OciRepoTester) getToken(client *http.Client, realm, service string, username, password *string) (string, error) {
	// Parse the realm URL
	authURL, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("failed to parse auth realm URL: %w", err)
	}

	// Build query parameters
	query := authURL.Query()
	if service != "" {
		query.Set("service", service)
	}

	query.Set("scope", "repository:"+uuid.New().String()+":pull")
	authURL.RawQuery = query.Encode()

	req, err := http.NewRequest("GET", authURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("failed to create auth request: %w", err)
	}

	// Add basic auth if credentials provided
	if username != nil && password != nil {
		req.SetBasicAuth(*username, *password)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to get token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if username != nil && password != nil {
			return "", fmt.Errorf("authentication failed: invalid credentials")
		}
		return "", fmt.Errorf("failed to get anonymous token: status %d", resp.StatusCode)
	}

	// Parse token from JSON response
	var tokenResp struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("failed to parse token response: %w", err)
	}

	if tokenResp.Token == "" && tokenResp.AccessToken == "" {
		return "", fmt.Errorf("empty token received")
	}
	if tokenResp.Token != "" {
		return tokenResp.Token, nil
	}
	if tokenResp.AccessToken != "" {
		return tokenResp.AccessToken, nil
	}

	return "", fmt.Errorf("empty token received")
}
