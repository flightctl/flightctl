package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	internalservice "github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/service/common"
	mainstore "github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
)

// Error types for distinguishing between validation and internal errors
var (
	// Validation errors (4xx)
	ErrImageExportNotReady               = errors.New("imageExport is not ready")
	ErrImageExportStatusNotReady         = errors.New("imageExport status is not ready")
	ErrImageExportReadyConditionNotFound = errors.New("imageExport ready condition not found")
	ErrImageExportManifestDigestNotSet   = errors.New("imageExport manifestDigest is not set")
	ErrInvalidManifestDigest             = errors.New("invalid manifest digest")
	ErrInvalidManifestLayerCount         = errors.New("invalid manifest layer count")
	ErrRepositoryNotFound                = errors.New("repository not found")

	// External service errors (5xx - Service Unavailable)
	ErrExternalServiceUnavailable = errors.New("external service unavailable")
)

// ImageExportService handles business logic for ImageExport resources
type ImageExportService interface {
	Create(ctx context.Context, orgId uuid.UUID, imageExport api.ImageExport) (*api.ImageExport, v1beta1.Status)
	Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageExport, v1beta1.Status)
	List(ctx context.Context, orgId uuid.UUID, params api.ListImageExportsParams) (*api.ImageExportList, v1beta1.Status)
	Delete(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageExport, v1beta1.Status)
	Download(ctx context.Context, orgId uuid.UUID, name string) (*ImageExportDownload, error)
	// Internal methods (not exposed via API)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, imageExport *api.ImageExport) (*api.ImageExport, error)
	UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error
}

// ImageExportDownload contains information for downloading an ImageExport artifact
type ImageExportDownload struct {
	RedirectURL string
	BlobReader  io.ReadCloser
	Headers     http.Header
	StatusCode  int
}

// imageExportService is the concrete implementation of ImageExportService
type imageExportService struct {
	imageExportStore store.ImageExportStore
	imageBuildStore  store.ImageBuildStore
	repositoryStore  mainstore.Repository
	eventHandler     *internalservice.EventHandler
	queueProducer    queues.QueueProducer
	log              logrus.FieldLogger
}

// NewImageExportService creates a new ImageExportService
func NewImageExportService(imageExportStore store.ImageExportStore, imageBuildStore store.ImageBuildStore, repositoryStore mainstore.Repository, eventHandler *internalservice.EventHandler, queueProducer queues.QueueProducer, log logrus.FieldLogger) ImageExportService {
	return &imageExportService{
		imageExportStore: imageExportStore,
		imageBuildStore:  imageBuildStore,
		repositoryStore:  repositoryStore,
		eventHandler:     eventHandler,
		queueProducer:    queueProducer,
		log:              log,
	}
}

func (s *imageExportService) Create(ctx context.Context, orgId uuid.UUID, imageExport api.ImageExport) (*api.ImageExport, v1beta1.Status) {
	// Don't set fields that are managed by the service
	imageExport.Status = nil
	NilOutManagedObjectMetaProperties(&imageExport.Metadata)

	// Validate input
	if errs, internalErr := s.validate(ctx, orgId, &imageExport); internalErr != nil {
		return nil, StatusInternalServerError(internalErr.Error())
	} else if len(errs) > 0 {
		return nil, StatusBadRequest(errors.Join(errs...).Error())
	}

	result, err := s.imageExportStore.Create(ctx, orgId, &imageExport)
	if err != nil {
		return result, StoreErrorToApiStatus(err, true, string(api.ResourceKindImageExport), imageExport.Metadata.Name)
	}

	// Create event separately (no transaction)
	var event *v1beta1.Event
	if result != nil && s.eventHandler != nil {
		event = common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, true, v1beta1.ResourceKind(string(api.ResourceKindImageExport)), lo.FromPtr(result.Metadata.Name), nil, s.log, nil)
		if event != nil {
			s.eventHandler.CreateEvent(ctx, orgId, event)
		}
	}

	// Enqueue event to imagebuild-queue for worker processing
	if result != nil && event != nil && s.queueProducer != nil {
		if err := s.enqueueImageExportEvent(ctx, orgId, event); err != nil {
			s.log.WithError(err).WithField("orgId", orgId).WithField("name", lo.FromPtr(result.Metadata.Name)).Error("failed to enqueue imageExport event")
			// Don't fail the creation if enqueue fails - the event can be retried later
		}
	}

	return result, StoreErrorToApiStatus(nil, true, string(api.ResourceKindImageExport), imageExport.Metadata.Name)
}

func (s *imageExportService) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageExport, v1beta1.Status) {
	result, err := s.imageExportStore.Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, string(api.ResourceKindImageExport), &name)
}

func (s *imageExportService) List(ctx context.Context, orgId uuid.UUID, params api.ListImageExportsParams) (*api.ImageExportList, v1beta1.Status) {
	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if !IsStatusOK(status) {
		return nil, status
	}

	result, err := s.imageExportStore.List(ctx, orgId, *listParams)
	if err == nil {
		return result, StatusOK()
	}

	var se *selector.SelectorError
	switch {
	case selector.AsSelectorError(err, &se):
		return nil, StatusBadRequest(se.Error())
	default:
		return nil, StatusInternalServerError(err.Error())
	}
}

func (s *imageExportService) Delete(ctx context.Context, orgId uuid.UUID, name string) (*api.ImageExport, v1beta1.Status) {
	result, err := s.imageExportStore.Delete(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, string(api.ResourceKindImageExport), &name)
}

// Internal methods (not exposed via API)

func (s *imageExportService) UpdateStatus(ctx context.Context, orgId uuid.UUID, imageExport *api.ImageExport) (*api.ImageExport, error) {
	// Update status
	result, err := s.imageExportStore.UpdateStatus(ctx, orgId, imageExport)
	if err != nil {
		return result, err
	}

	// Create event for status update
	var event *v1beta1.Event
	if result != nil && result.Metadata.Name != nil && s.eventHandler != nil {
		// Create a simple status update event since status is not in UpdatedFields enum
		event = domain.GetBaseEvent(
			ctx,
			v1beta1.ResourceKind(string(api.ResourceKindImageExport)),
			*result.Metadata.Name,
			domain.EventReasonResourceUpdated,
			fmt.Sprintf("%s status was updated successfully.", string(api.ResourceKindImageExport)),
			nil,
		)
		if event != nil {
			s.eventHandler.CreateEvent(ctx, orgId, event)
		}
	}

	// Enqueue event to imagebuild-queue if image is ready (Completed)
	if result != nil && event != nil && s.queueProducer != nil {
		// Check if Ready condition is True with reason Completed
		if result.Status != nil && result.Status.Conditions != nil {
			readyCondition := api.FindImageExportStatusCondition(*result.Status.Conditions, api.ImageExportConditionTypeReady)
			if readyCondition != nil &&
				readyCondition.Status == v1beta1.ConditionStatusTrue &&
				readyCondition.Reason == string(api.ImageExportConditionReasonCompleted) {
				if err := s.enqueueImageExportEvent(ctx, orgId, event); err != nil {
					s.log.WithError(err).WithField("orgId", orgId).WithField("name", *result.Metadata.Name).Error("failed to enqueue imageExport event")
					// Don't fail the update if enqueue fails - the event can be retried later
				}
			}
		}
	}

	return result, err
}

func (s *imageExportService) UpdateLastSeen(ctx context.Context, orgId uuid.UUID, name string, timestamp time.Time) error {
	return s.imageExportStore.UpdateLastSeen(ctx, orgId, name, timestamp)
}

func (s *imageExportService) Download(ctx context.Context, orgId uuid.UUID, name string) (*ImageExportDownload, error) {
	log := s.log.WithFields(logrus.Fields{"orgId": orgId, "name": name})
	log.Info("Starting image export download")

	// Fetch ImageExport from database
	imageExport, err := s.imageExportStore.Get(ctx, orgId, name)
	if err != nil {
		log.WithError(err).Error("Failed to get image export")
		return nil, err
	}

	if err := validateImageExportForDownload(imageExport); err != nil {
		return nil, err
	}
	manifestDigestStr := *imageExport.Status.ManifestDigest
	log.WithField("manifestDigest", manifestDigestStr).Debug("Found manifest digest")

	// Get the ImageBuild to use its destination
	sourceType, err := imageExport.Spec.Source.Discriminator()
	if err != nil {
		return nil, fmt.Errorf("failed to determine source type: %w", err)
	}
	if sourceType != string(api.ImageExportSourceTypeImageBuild) {
		return nil, fmt.Errorf("unexpected source type: %q", sourceType)
	}

	source, err := imageExport.Spec.Source.AsImageBuildRefSource()
	if err != nil {
		return nil, fmt.Errorf("failed to parse imageBuild source: %w", err)
	}

	imageBuild, err := s.imageBuildStore.Get(ctx, orgId, source.ImageBuildRef)
	if err != nil {
		log.WithError(err).WithField("imageBuildRef", source.ImageBuildRef).Error("Failed to get ImageBuild")
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return nil, fmt.Errorf("ImageBuild %q not found: %w", source.ImageBuildRef, err)
		}
		return nil, fmt.Errorf("failed to get ImageBuild %q: %w", source.ImageBuildRef, err)
	}

	// Fetch destination repository from database
	repo, err := s.repositoryStore.Get(ctx, orgId, imageBuild.Spec.Destination.Repository)
	if err != nil {
		log.WithError(err).WithField("destinationRepo", imageBuild.Spec.Destination.Repository).Error("Failed to get destination repository")
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return nil, fmt.Errorf("%w: %w", ErrRepositoryNotFound, err)
		}
		// Return store error as-is (will be handled by transport layer)
		return nil, err
	}

	ociSpec, err := repo.Spec.AsOciRepoSpec()
	if err != nil {
		log.WithError(err).WithField("destinationRepo", imageBuild.Spec.Destination.Repository).Error("Failed to parse OCI repository spec")
		return nil, fmt.Errorf("failed to parse OCI repository spec: %w", err)
	}

	// Setup repository reference and authentication
	repoRef, scheme, registryHostname, err := s.setupRepositoryReference(ctx, &ociSpec, imageBuild.Spec.Destination.ImageName, log)
	if err != nil {
		return nil, err
	}

	// Fetch and parse manifest
	manifest, err := s.fetchAndParseManifest(ctx, repoRef, manifestDigestStr, log)
	if err != nil {
		return nil, err
	}

	// Validate manifest structure
	if len(manifest.Layers) != 1 {
		log.WithFields(logrus.Fields{"manifestDigest": manifestDigestStr, "layerCount": len(manifest.Layers)}).Error("Manifest has incorrect number of layers")
		return nil, fmt.Errorf("%w: manifest must have exactly one layer, found %d", ErrInvalidManifestLayerCount, len(manifest.Layers))
	}

	layerDigestStr := manifest.Layers[0].Digest.String()
	log.WithFields(logrus.Fields{
		"manifestDigest": manifestDigestStr, "layerDigest": layerDigestStr,
		"layerSize": manifest.Layers[0].Size, "layerMediaType": manifest.Layers[0].MediaType,
	}).Debug("Extracted layer information from manifest")

	// Construct blob URL
	path, err := url.JoinPath("/v2", imageBuild.Spec.Destination.ImageName, "blobs", layerDigestStr)
	if err != nil {
		log.WithError(err).Error("Failed to construct blob URL path")
		return nil, fmt.Errorf("failed to construct blob URL path: %w", err)
	}
	blobURL := &url.URL{
		Scheme: scheme,
		Host:   registryHostname,
		Path:   path,
	}
	blobURLStr := blobURL.String()
	log.WithFields(logrus.Fields{"blobURL": blobURLStr, "layerDigest": layerDigestStr}).Debug("Constructed blob URL")

	// Create HTTP client with TLS configuration
	httpClient, err := s.createHTTPClient(&ociSpec)
	if err != nil {
		log.WithError(err).Error("Failed to create HTTP client")
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	// Make GET request to fetch blob
	log.WithFields(logrus.Fields{"blobURL": blobURLStr, "method": "GET"}).Debug("Making GET request to fetch blob")
	getReq, err := http.NewRequestWithContext(ctx, "GET", blobURLStr, nil)
	if err != nil {
		log.WithError(err).WithField("blobURL", blobURLStr).Error("Failed to create GET request")
		return nil, fmt.Errorf("failed to create GET request: %w", err)
	}

	// Add authentication if available
	if err := s.addAuthenticationToRequest(ctx, getReq, httpClient, scheme, registryHostname, imageBuild.Spec.Destination.ImageName, &ociSpec, log); err != nil {
		return nil, err
	}

	getResp, err := httpClient.Do(getReq)
	if err != nil {
		log.WithError(err).WithField("blobURL", blobURLStr).Error("Failed to make GET request to external service")
		return nil, fmt.Errorf("%w: failed to make GET request: %w", ErrExternalServiceUnavailable, err)
	}

	log.WithFields(logrus.Fields{"blobURL": blobURLStr, "statusCode": getResp.StatusCode}).Debug("Received GET response")

	return s.handleBlobResponse(getResp, blobURLStr, log)
}

// setupRepositoryReference creates a repository reference and configures authentication
func (s *imageExportService) setupRepositoryReference(ctx context.Context, ociSpec *v1beta1.OciRepoSpec, imageName string, log logrus.FieldLogger) (*remote.Repository, string, string, error) {
	scheme := "https"
	if ociSpec.Scheme != nil {
		scheme = string(*ociSpec.Scheme)
	}
	registryHostname := ociSpec.Registry
	destRef := fmt.Sprintf("%s/%s", registryHostname, imageName)

	log.WithFields(logrus.Fields{
		"destRef": destRef, "scheme": scheme, "registryHostname": registryHostname,
		"imageName": imageName,
	}).Debug("Creating repository reference")

	repoRef, err := remote.NewRepository(destRef)
	if err != nil {
		log.WithError(err).WithField("destRef", destRef).Error("Failed to create repository reference")
		return nil, "", "", fmt.Errorf("failed to create repository reference: %w", err)
	}

	// Set up authentication if credentials are provided
	if ociSpec.OciAuth != nil {
		dockerAuth, err := ociSpec.OciAuth.AsDockerAuth()
		if err == nil && dockerAuth.Username != "" && dockerAuth.Password != "" {
			repoRef.Client = &auth.Client{
				Credential: auth.StaticCredential(registryHostname, auth.Credential{
					Username: dockerAuth.Username,
					Password: dockerAuth.Password,
				}),
			}
			log.WithFields(logrus.Fields{"registryHostname": registryHostname, "username": dockerAuth.Username}).Debug("Configured authentication for repository")
		}
	} else {
		log.Debug("No authentication configured for repository")
	}

	return repoRef, scheme, registryHostname, nil
}

// fetchAndParseManifest fetches and parses the OCI manifest
func (s *imageExportService) fetchAndParseManifest(ctx context.Context, repoRef *remote.Repository, manifestDigestStr string, log logrus.FieldLogger) (*ocispec.Manifest, error) {
	manifestDigest, err := digest.Parse(manifestDigestStr)
	if err != nil {
		log.WithError(err).WithField("manifestDigest", manifestDigestStr).Error("Failed to parse manifest digest")
		return nil, fmt.Errorf("%w: %w", ErrInvalidManifestDigest, err)
	}

	// Try to resolve the manifest reference using the digest
	log.WithField("manifestDigest", manifestDigestStr).Debug("Attempting to resolve manifest")
	manifestDesc, err := repoRef.Resolve(ctx, manifestDigestStr)
	if err != nil {
		log.WithError(err).WithField("manifestDigest", manifestDigestStr).Warn("Failed to resolve manifest, will try Fetch directly")
		manifestDesc = ocispec.Descriptor{
			Digest:    manifestDigest,
			MediaType: ocispec.MediaTypeImageManifest,
		}
	} else {
		log.WithFields(logrus.Fields{"manifestDigest": manifestDigestStr, "mediaType": manifestDesc.MediaType, "size": manifestDesc.Size}).Debug("Successfully resolved manifest")
	}

	// Fetch manifest
	log.WithFields(logrus.Fields{"manifestDigest": manifestDigestStr, "mediaType": manifestDesc.MediaType}).Debug("Fetching manifest")
	manifestReader, err := repoRef.Fetch(ctx, manifestDesc)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{"manifestDigest": manifestDigestStr, "mediaType": manifestDesc.MediaType}).Error("Failed to fetch manifest from external service")
		return nil, fmt.Errorf("%w: failed to fetch manifest: %w", ErrExternalServiceUnavailable, err)
	}
	defer manifestReader.Close()

	manifestBytes, err := io.ReadAll(manifestReader)
	if err != nil {
		log.WithError(err).WithField("manifestDigest", manifestDigestStr).Error("Failed to read manifest")
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	log.WithFields(logrus.Fields{"manifestDigest": manifestDigestStr, "manifestSize": len(manifestBytes)}).Debug("Read manifest bytes")

	// Parse manifest
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		log.WithError(err).WithField("manifestDigest", manifestDigestStr).Error("Failed to parse manifest JSON")
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	log.WithFields(logrus.Fields{"manifestDigest": manifestDigestStr, "layerCount": len(manifest.Layers), "mediaType": manifest.MediaType}).Debug("Parsed manifest")

	return &manifest, nil
}

// createHTTPClient creates an HTTP client with TLS configuration
func (s *imageExportService) createHTTPClient(ociSpec *v1beta1.OciRepoSpec) (*http.Client, error) {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if ociSpec.SkipServerVerification != nil && *ociSpec.SkipServerVerification {
		tlsConfig.InsecureSkipVerify = true
	}
	if ociSpec.CaCrt != nil {
		ca, err := base64.StdEncoding.DecodeString(*ociSpec.CaCrt)
		if err != nil {
			return nil, fmt.Errorf("createHTTPClient: decode CA: %w", err)
		}
		rootCAs, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("createHTTPClient: system cert pool: %w", err)
		}
		if rootCAs == nil {
			rootCAs = x509.NewCertPool()
		}
		if !rootCAs.AppendCertsFromPEM(ca) {
			return nil, fmt.Errorf("createHTTPClient: failed to append CA certificates from PEM")
		}
		tlsConfig.RootCAs = rootCAs
	}

	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// addAuthenticationToRequest adds authentication headers to the request if needed
func (s *imageExportService) addAuthenticationToRequest(ctx context.Context, req *http.Request, client *http.Client, scheme, registryHostname, repoName string, ociSpec *v1beta1.OciRepoSpec, log logrus.FieldLogger) error {
	if ociSpec.OciAuth == nil {
		return nil
	}

	dockerAuth, err := ociSpec.OciAuth.AsDockerAuth()
	if err != nil || dockerAuth.Username == "" || dockerAuth.Password == "" {
		return nil
	}

	log.WithFields(logrus.Fields{"registryHostname": registryHostname, "repoName": repoName}).Debug("Getting registry token for authentication")
	token, err := s.getRegistryToken(ctx, client, scheme, registryHostname, repoName, dockerAuth.Username, dockerAuth.Password)
	if err != nil {
		log.WithError(err).WithField("registryHostname", registryHostname).Error("Failed to get registry token from external service")
		return fmt.Errorf("%w: failed to get registry token: %w", ErrExternalServiceUnavailable, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		log.WithField("hasToken", true).Debug("Added bearer token to GET request")
	}

	return nil
}

// handleBlobResponse handles the HTTP response from the blob endpoint
func (s *imageExportService) handleBlobResponse(resp *http.Response, blobURL string, log logrus.FieldLogger) (*ImageExportDownload, error) {
	// Handle redirect (3xx)
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		resp.Body.Close()
		redirectURL := resp.Header.Get("Location")
		if redirectURL == "" {
			log.WithField("statusCode", resp.StatusCode).Error("Redirect response missing Location header")
			return nil, errors.New("redirect response missing Location header")
		}
		log.WithFields(logrus.Fields{"statusCode": resp.StatusCode, "redirectURL": redirectURL}).Info("Returning redirect response")
		return &ImageExportDownload{
			RedirectURL: redirectURL,
			StatusCode:  resp.StatusCode,
		}, nil
	}

	// Handle 200 OK - stream blob content
	if resp.StatusCode == http.StatusOK {
		contentLength := resp.Header.Get("Content-Length")
		log.WithFields(logrus.Fields{"blobURL": blobURL, "statusCode": resp.StatusCode, "contentLength": contentLength}).Info("Successfully fetched blob, returning stream")
		return &ImageExportDownload{
			BlobReader: resp.Body,
			Headers:    resp.Header,
			StatusCode: resp.StatusCode,
		}, nil
	}

	// Handle unexpected status codes
	resp.Body.Close()
	log.WithFields(logrus.Fields{"blobURL": blobURL, "statusCode": resp.StatusCode}).Error("Unexpected status code from external service")
	return nil, fmt.Errorf("%w: unexpected status code from blob endpoint: %d", ErrExternalServiceUnavailable, resp.StatusCode)
}

// getRegistryToken gets a bearer token for registry authentication
func (s *imageExportService) getRegistryToken(ctx context.Context, client *http.Client, scheme, registryHostname, repoName, username, password string) (string, error) {
	// First, try to access /v2/ to get Www-Authenticate header
	v2URL := fmt.Sprintf("%s://%s/v2/", scheme, registryHostname)
	req, err := http.NewRequestWithContext(ctx, "GET", v2URL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to connect to registry: %w", err)
	}
	defer resp.Body.Close()

	// If already authenticated, return empty token (will use basic auth)
	if resp.StatusCode == http.StatusOK {
		return "", nil
	}

	// Parse Www-Authenticate header
	wwwAuth := resp.Header.Get("Www-Authenticate")
	if wwwAuth == "" {
		return "", fmt.Errorf("missing Www-Authenticate header")
	}

	realm, service, err := parseWwwAuthenticate(wwwAuth)
	if err != nil {
		return "", fmt.Errorf("failed to parse Www-Authenticate header: %w", err)
	}

	// Get token from auth endpoint
	authURL, err := url.Parse(realm)
	if err != nil {
		return "", fmt.Errorf("failed to parse auth realm URL: %w", err)
	}

	query := authURL.Query()
	if service != "" {
		query.Set("service", service)
	}
	// Set scope for the specific repository
	scope := fmt.Sprintf("repository:%s:pull", repoName)
	query.Set("scope", scope)
	authURL.RawQuery = query.Encode()

	tokenReq, err := http.NewRequestWithContext(ctx, "GET", authURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("failed to create token request: %w", err)
	}

	tokenReq.SetBasicAuth(username, password)

	tokenResp, err := client.Do(tokenReq)
	if err != nil {
		return "", fmt.Errorf("%w: failed to get token: %w", ErrExternalServiceUnavailable, err)
	}
	defer tokenResp.Body.Close()

	if tokenResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("authentication failed: status %d", tokenResp.StatusCode)
	}

	var tokenData struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
		return "", fmt.Errorf("failed to parse token response: %w", err)
	}

	if tokenData.Token != "" {
		return tokenData.Token, nil
	}
	if tokenData.AccessToken != "" {
		return tokenData.AccessToken, nil
	}

	return "", fmt.Errorf("empty token received")
}

// validateImageExportForDownload validates that an ImageExport is ready for download.
// This function does not perform any database calls.
// Returns known error types that can be checked with errors.Is().
func validateImageExportForDownload(imageExport *api.ImageExport) error {
	// Validate Ready condition is True
	if imageExport.Status == nil || imageExport.Status.Conditions == nil {
		return ErrImageExportStatusNotReady
	}
	readyCondition := api.FindImageExportStatusCondition(*imageExport.Status.Conditions, api.ImageExportConditionTypeReady)
	if readyCondition == nil {
		return ErrImageExportReadyConditionNotFound
	}
	if readyCondition.Status != v1beta1.ConditionStatusTrue {
		return fmt.Errorf("%w (status: %s, reason: %s)", ErrImageExportNotReady, readyCondition.Status, readyCondition.Reason)
	}

	// Check manifestDigest exists
	if imageExport.Status.ManifestDigest == nil || *imageExport.Status.ManifestDigest == "" {
		return ErrImageExportManifestDigestNotSet
	}

	return nil
}

// parseWwwAuthenticate parses the Www-Authenticate header to extract realm and service
func parseWwwAuthenticate(header string) (realm, service string, err error) {
	// Example: Bearer realm="https://quay.io/v2/auth",service="quay.io"
	if !strings.HasPrefix(header, "Bearer ") {
		return "", "", fmt.Errorf("unsupported authentication scheme")
	}
	header = strings.TrimPrefix(header, "Bearer ")

	// Parse key="value" pairs, handling commas inside quoted strings
	i := 0
	for i < len(header) {
		// Skip whitespace
		for i < len(header) && (header[i] == ' ' || header[i] == '\t') {
			i++
		}
		if i >= len(header) {
			break
		}

		// Find the key (everything up to '=')
		keyStart := i
		for i < len(header) && header[i] != '=' {
			i++
		}
		if i >= len(header) {
			break
		}
		key := strings.TrimSpace(header[keyStart:i])
		i++ // skip '='

		// Skip whitespace after '='
		for i < len(header) && (header[i] == ' ' || header[i] == '\t') {
			i++
		}
		if i >= len(header) {
			break
		}

		// Parse the value (quoted string)
		if header[i] != '"' {
			return "", "", fmt.Errorf("expected quoted value for key %q", key)
		}
		i++ // skip opening quote

		// Extract value, handling escaped quotes
		var value strings.Builder
		for i < len(header) {
			if header[i] == '\\' && i+1 < len(header) && header[i+1] == '"' {
				// Escaped quote
				value.WriteByte('"')
				i += 2
			} else if header[i] == '"' {
				// End of quoted string
				i++
				break
			} else {
				value.WriteByte(header[i])
				i++
			}
		}

		// Store the value
		parsedValue := value.String()
		if key == "realm" {
			realm = parsedValue
		} else if key == "service" {
			service = parsedValue
		}

		// Skip whitespace and comma separator
		for i < len(header) && (header[i] == ' ' || header[i] == '\t') {
			i++
		}
		if i < len(header) && header[i] == ',' {
			i++ // skip comma
		}
	}

	if realm == "" {
		return "", "", fmt.Errorf("realm not found in Www-Authenticate header")
	}

	return realm, service, nil
}

// validate performs validation on an ImageExport resource
// Returns validation errors (4xx) and internal errors (5xx) separately
func (s *imageExportService) validate(ctx context.Context, orgId uuid.UUID, imageExport *api.ImageExport) ([]error, error) {
	var errs []error

	if lo.FromPtr(imageExport.Metadata.Name) == "" {
		errs = append(errs, errors.New("metadata.name is required"))
	}

	// Validate source - uses discriminator pattern
	sourceType, err := imageExport.Spec.Source.Discriminator()
	if err != nil {
		errs = append(errs, errors.New("spec.source.type is required"))
	} else {
		switch sourceType {
		case string(api.ImageExportSourceTypeImageBuild):
			source, err := imageExport.Spec.Source.AsImageBuildRefSource()
			if err != nil {
				errs = append(errs, errors.New("invalid imageBuild source"))
			} else if source.ImageBuildRef == "" {
				errs = append(errs, errors.New("spec.source.imageBuildRef is required for imageBuild source type"))
			} else {
				// Check that the referenced ImageBuild exists
				imageBuild, err := s.imageBuildStore.Get(ctx, orgId, source.ImageBuildRef)
				if errors.Is(err, flterrors.ErrResourceNotFound) {
					errs = append(errs, fmt.Errorf("spec.source.imageBuildRef: ImageBuild %q not found", source.ImageBuildRef))
				} else if err != nil {
					return nil, fmt.Errorf("failed to get ImageBuild %q: %w", source.ImageBuildRef, err)
				} else {
					// Validate that the ImageBuild has a destination configured
					if imageBuild.Spec.Destination.Repository == "" {
						errs = append(errs, fmt.Errorf("spec.source.imageBuildRef: ImageBuild %q does not have a destination repository configured", source.ImageBuildRef))
					}
					if imageBuild.Spec.Destination.ImageName == "" {
						errs = append(errs, fmt.Errorf("spec.source.imageBuildRef: ImageBuild %q does not have a destination imageName configured", source.ImageBuildRef))
					}
					if imageBuild.Spec.Destination.ImageTag == "" {
						errs = append(errs, fmt.Errorf("spec.source.imageBuildRef: ImageBuild %q does not have a destination imageTag configured", source.ImageBuildRef))
					}
				}
			}
		default:
			errs = append(errs, errors.New("spec.source.type must be 'imageBuild'"))
		}
	}

	// Validate formats
	if imageExport.Spec.Format == "" {
		errs = append(errs, errors.New("spec.format is required"))
	}

	return errs, nil
}

// enqueueImageExportEvent enqueues an event to the imagebuild-queue
func (s *imageExportService) enqueueImageExportEvent(ctx context.Context, orgId uuid.UUID, event *v1beta1.Event) error {
	if event == nil {
		return errors.New("event is nil")
	}

	// Create EventWithOrgId structure for the queue
	eventWithOrgId := worker_client.EventWithOrgId{
		OrgId: orgId,
		Event: *event,
	}

	payload, err := json.Marshal(eventWithOrgId)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	// Use creation timestamp if available, otherwise use current time
	var timestamp int64
	if event.Metadata.CreationTimestamp != nil {
		timestamp = event.Metadata.CreationTimestamp.UnixMicro()
	} else {
		timestamp = time.Now().UnixMicro()
	}

	if err := s.queueProducer.Enqueue(ctx, payload, timestamp); err != nil {
		return fmt.Errorf("failed to enqueue event: %w", err)
	}

	s.log.WithField("orgId", orgId).WithField("name", event.InvolvedObject.Name).Info("enqueued imageExport event")
	return nil
}
