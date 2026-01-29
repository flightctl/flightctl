package e2e

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	imagebuilderapi "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"sigs.k8s.io/yaml"
)

// Log stream errors
var (
	// ErrLogStreamTimeout indicates the log stream timed out before completion
	ErrLogStreamTimeout = errors.New("log stream timeout")
	// ErrLogStreamUnexpectedClose indicates the stream closed without completion marker
	ErrLogStreamUnexpectedClose = errors.New("log stream closed unexpectedly")
)

// CreateImageBuild creates an ImageBuild resource with the given name and spec.
func (h *Harness) CreateImageBuild(name string, spec imagebuilderapi.ImageBuildSpec) (*imagebuilderapi.ImageBuild, error) {
	return h.CreateImageBuildWithLabels(name, spec, nil)
}

// CreateImageBuildWithLabels creates an ImageBuild resource with the given name, spec, and labels.
func (h *Harness) CreateImageBuildWithLabels(name string, spec imagebuilderapi.ImageBuildSpec, labels map[string]string) (*imagebuilderapi.ImageBuild, error) {
	imageBuild := imagebuilderapi.ImageBuild{
		ApiVersion: imagebuilderapi.ImageBuildAPIVersion,
		Kind:       string(imagebuilderapi.ResourceKindImageBuild),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: spec,
	}

	// Add custom labels if provided
	if labels != nil {
		imageBuild.Metadata.Labels = &labels
	}

	// Add test label to track resources for cleanup
	h.addTestLabelToResource(&imageBuild.Metadata)

	response, err := h.ImageBuilderClient.CreateImageBuildWithResponse(h.Context, imageBuild)
	if err != nil {
		return nil, fmt.Errorf("failed to create ImageBuild: %w", err)
	}

	if response.JSON201 == nil {
		return nil, fmt.Errorf("failed to create ImageBuild %s: status=%d body=%s",
			name, response.StatusCode(), string(response.Body))
	}

	GinkgoWriter.Printf("Created ImageBuild: %s\n", name)
	return response.JSON201, nil
}

// GetImageBuild retrieves an ImageBuild resource by name.
func (h *Harness) GetImageBuild(name string) (*imagebuilderapi.ImageBuild, error) {
	response, err := h.ImageBuilderClient.GetImageBuildWithResponse(h.Context, name, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get ImageBuild %s: %w", name, err)
	}

	if response.JSON200 == nil {
		return nil, fmt.Errorf("ImageBuild %s not found: status=%d body=%s",
			name, response.StatusCode(), string(response.Body))
	}

	return response.JSON200, nil
}

// DeleteImageBuild deletes an ImageBuild resource by name.
func (h *Harness) DeleteImageBuild(name string) error {
	response, err := h.ImageBuilderClient.DeleteImageBuildWithResponse(h.Context, name)
	if err != nil {
		return fmt.Errorf("failed to delete ImageBuild %s: %w", name, err)
	}

	if response.StatusCode() != 200 && response.StatusCode() != 404 {
		return fmt.Errorf("failed to delete ImageBuild %s: status=%d body=%s",
			name, response.StatusCode(), string(response.Body))
	}

	GinkgoWriter.Printf("Deleted ImageBuild: %s\n", name)
	return nil
}

// CancelImageBuild cancels an in-progress ImageBuild.
func (h *Harness) CancelImageBuild(name string) error {
	response, err := h.ImageBuilderClient.CancelImageBuildWithResponse(h.Context, name)
	if err != nil {
		return fmt.Errorf("failed to cancel ImageBuild %s: %w", name, err)
	}

	if response.StatusCode() != 200 {
		return fmt.Errorf("failed to cancel ImageBuild %s: status=%d body=%s",
			name, response.StatusCode(), string(response.Body))
	}

	GinkgoWriter.Printf("Canceled ImageBuild: %s\n", name)
	return nil
}

// ListImageBuilds lists all ImageBuild resources.
func (h *Harness) ListImageBuilds(params *imagebuilderapi.ListImageBuildsParams) (*imagebuilderapi.ImageBuildList, error) {
	response, err := h.ImageBuilderClient.ListImageBuildsWithResponse(h.Context, params)
	if err != nil {
		return nil, fmt.Errorf("failed to list ImageBuilds: %w", err)
	}

	if response.JSON200 == nil {
		return nil, fmt.Errorf("failed to list ImageBuilds: status=%d body=%s",
			response.StatusCode(), string(response.Body))
	}

	return response.JSON200, nil
}

// ListImageExports lists all ImageExport resources.
func (h *Harness) ListImageExports(params *imagebuilderapi.ListImageExportsParams) (*imagebuilderapi.ImageExportList, error) {
	response, err := h.ImageBuilderClient.ListImageExportsWithResponse(h.Context, params)
	if err != nil {
		return nil, fmt.Errorf("failed to list ImageExports: %w", err)
	}

	if response.JSON200 == nil {
		return nil, fmt.Errorf("failed to list ImageExports: status=%d body=%s",
			response.StatusCode(), string(response.Body))
	}

	return response.JSON200, nil
}

// GetImageBuildLogs retrieves the logs for an ImageBuild.
func (h *Harness) GetImageBuildLogs(name string) (string, error) {
	params := &imagebuilderapi.GetImageBuildLogParams{
		Follow: lo.ToPtr(false),
	}

	response, err := h.ImageBuilderClient.GetImageBuildLog(h.Context, name, params)
	if err != nil {
		return "", fmt.Errorf("failed to get ImageBuild logs for %s: %w", name, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read ImageBuild logs for %s: %w", name, err)
	}

	return string(body), nil
}

// GetImageExportLogs retrieves the logs for an ImageExport.
func (h *Harness) GetImageExportLogs(name string) (string, error) {
	params := &imagebuilderapi.GetImageExportLogParams{
		Follow: lo.ToPtr(false),
	}

	response, err := h.ImageBuilderClient.GetImageExportLog(h.Context, name, params)
	if err != nil {
		return "", fmt.Errorf("failed to get ImageExport logs for %s: %w", name, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read ImageExport logs for %s: %w", name, err)
	}

	return string(body), nil
}

// StreamImageBuildLogsWithTimeout streams logs for an ImageBuild with follow mode for a limited time.
// Returns the logs captured during the streaming period.
func (h *Harness) StreamImageBuildLogsWithTimeout(name string, streamDuration time.Duration) (string, error) {
	GinkgoWriter.Printf("Streaming logs for ImageBuild %s for %v\n", name, streamDuration)

	ctx, cancel := context.WithTimeout(h.Context, streamDuration)
	defer cancel()

	params := &imagebuilderapi.GetImageBuildLogParams{
		Follow: lo.ToPtr(true),
	}

	response, err := h.ImageBuilderClient.GetImageBuildLog(ctx, name, params)
	if err != nil {
		return "", fmt.Errorf("failed to start log stream for ImageBuild %s: %w", name, err)
	}
	defer response.Body.Close()

	if response.StatusCode != 200 {
		body, _ := io.ReadAll(response.Body)
		return "", fmt.Errorf("failed to get log stream for ImageBuild %s: status=%d body=%s",
			name, response.StatusCode, string(body))
	}

	// Read logs until context timeout
	var logs strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := response.Body.Read(buf)
		if n > 0 {
			logs.Write(buf[:n])
		}
		if err != nil {
			if err == io.EOF || ctx.Err() != nil {
				break
			}
			// Continue on other errors as stream may still have data
		}
	}

	return logs.String(), nil
}

// StreamImageExportLogsWithTimeout streams logs for an ImageExport with follow mode for a limited time.
// Returns the logs captured during the streaming period.
func (h *Harness) StreamImageExportLogsWithTimeout(name string, streamDuration time.Duration) (string, error) {
	GinkgoWriter.Printf("Streaming logs for ImageExport %s for %v\n", name, streamDuration)

	ctx, cancel := context.WithTimeout(h.Context, streamDuration)
	defer cancel()

	params := &imagebuilderapi.GetImageExportLogParams{
		Follow: lo.ToPtr(true),
	}

	response, err := h.ImageBuilderClient.GetImageExportLog(ctx, name, params)
	if err != nil {
		return "", fmt.Errorf("failed to start log stream for ImageExport %s: %w", name, err)
	}
	defer response.Body.Close()

	if response.StatusCode != 200 {
		body, _ := io.ReadAll(response.Body)
		return "", fmt.Errorf("failed to get log stream for ImageExport %s: status=%d body=%s",
			name, response.StatusCode, string(body))
	}

	// Read logs until context timeout
	var logs strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := response.Body.Read(buf)
		if n > 0 {
			logs.Write(buf[:n])
		}
		if err != nil {
			if err == io.EOF || ctx.Err() != nil {
				break
			}
			// Continue on other errors as stream may still have data
		}
	}

	return logs.String(), nil
}

// WaitForImageBuildCondition waits for an ImageBuild to reach a specific condition reason.
func (h *Harness) WaitForImageBuildCondition(name string, expectedReason imagebuilderapi.ImageBuildConditionReason, timeout, polling time.Duration) error {
	Eventually(func() (string, error) {
		imageBuild, err := h.GetImageBuild(name)
		if err != nil {
			return "", err
		}

		if imageBuild.Status == nil || imageBuild.Status.Conditions == nil {
			return string(imagebuilderapi.ImageBuildConditionReasonPending), nil
		}

		for _, cond := range *imageBuild.Status.Conditions {
			if cond.Type == imagebuilderapi.ImageBuildConditionTypeReady {
				logrus.Infof("ImageBuild %s condition: reason=%s status=%s message=%s",
					name, cond.Reason, cond.Status, cond.Message)
				return cond.Reason, nil
			}
		}

		return string(imagebuilderapi.ImageBuildConditionReasonPending), nil
	}, timeout, polling).Should(Equal(string(expectedReason)),
		fmt.Sprintf("Expected ImageBuild %s to have condition reason %s", name, expectedReason))

	return nil
}

// WaitForImageBuildCompletion waits for an ImageBuild to complete successfully.
func (h *Harness) WaitForImageBuildCompletion(name string, timeout time.Duration) (*imagebuilderapi.ImageBuild, error) {
	err := h.WaitForImageBuildCondition(name, imagebuilderapi.ImageBuildConditionReasonCompleted, timeout, 5*time.Second)
	if err != nil {
		return nil, err
	}
	return h.GetImageBuild(name)
}

// WaitForImageBuildFailure waits for an ImageBuild to fail.
func (h *Harness) WaitForImageBuildFailure(name string, timeout time.Duration) (*imagebuilderapi.ImageBuild, error) {
	err := h.WaitForImageBuildCondition(name, imagebuilderapi.ImageBuildConditionReasonFailed, timeout, 5*time.Second)
	if err != nil {
		return nil, err
	}
	return h.GetImageBuild(name)
}

// WaitForImageBuildProcessing waits for an ImageBuild to start processing (leave Pending state).
// Returns the ImageBuild once it's in Building, Pushing, Completed, Failed, or other non-Pending state.
func (h *Harness) WaitForImageBuildProcessing(name string, timeout, polling time.Duration) (*imagebuilderapi.ImageBuild, error) {
	GinkgoWriter.Printf("Waiting for ImageBuild %s to start processing (timeout: %v, polling: %v)\n", name, timeout, polling)

	var lastReason string
	Eventually(func() (bool, error) {
		imageBuild, err := h.GetImageBuild(name)
		if err != nil {
			return false, err
		}

		reason, _ := h.getImageBuildReadyCondition(imageBuild)
		lastReason = reason

		// Build has started processing if it's no longer Pending
		if reason != "" && reason != string(imagebuilderapi.ImageBuildConditionReasonPending) {
			GinkgoWriter.Printf("ImageBuild %s started processing: reason=%s\n", name, reason)
			return true, nil
		}

		GinkgoWriter.Printf("ImageBuild %s still pending...\n", name)
		return false, nil
	}, timeout, polling).Should(BeTrue(),
		fmt.Sprintf("Expected ImageBuild %s to start processing, but still in state: %s", name, lastReason))

	return h.GetImageBuild(name)
}

// handleSSELogStream processes Server-Sent Events log stream (matching CLI behavior).
// Returns:
//   - nil: stream completed successfully (received completion marker)
//   - ErrLogStreamTimeout: context timeout/cancellation
//   - ErrLogStreamUnexpectedClose: stream closed without completion marker
//   - other error: read error
func (h *Harness) handleSSELogStream(ctx context.Context, name string, body io.Reader) error {
	scanner := bufio.NewScanner(body)
	// Increase buffer size to handle very long log lines
	const maxScannerBuffer = 1024 * 1024 // 1MB
	scanner.Buffer(make([]byte, 0, maxScannerBuffer), maxScannerBuffer)

	var currentData strings.Builder
	lineCount := 0
	streamCompleted := false

	for scanner.Scan() {
		// Check for context cancellation (timeout)
		if ctx.Err() != nil {
			GinkgoWriter.Printf("Log stream timeout for ImageBuild %s (read %d lines)\n", name, lineCount)
			return fmt.Errorf("ImageBuild %s: %w: %w", name, ErrLogStreamTimeout, ctx.Err())
		}

		line := scanner.Text()

		// SSE format: "data: {content}\n\n"
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			// Check for completion marker
			if data == imagebuilderapi.LogStreamCompleteMarker {
				streamCompleted = true
				break
			}
			currentData.WriteString(data)
		} else if line == "" {
			// Empty line - SSE delimiter, output accumulated data
			if currentData.Len() > 0 {
				lineCount++
				data := currentData.String()
				if !strings.HasSuffix(data, "\n") {
					data += "\n"
				}
				GinkgoWriter.Printf("[ImageBuild %s] %s", name, data)
				currentData.Reset()
			}
		} else {
			// Regular log line (not SSE formatted)
			lineCount++
			GinkgoWriter.Printf("[ImageBuild %s] %s\n", name, line)
		}
	}

	// Output any remaining data
	if currentData.Len() > 0 {
		lineCount++
		data := currentData.String()
		if !strings.HasSuffix(data, "\n") {
			data += "\n"
		}
		GinkgoWriter.Printf("[ImageBuild %s] %s", name, data)
	}

	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			GinkgoWriter.Printf("Log stream timeout for ImageBuild %s (read %d lines)\n", name, lineCount)
			return fmt.Errorf("ImageBuild %s: %w: %w", name, ErrLogStreamTimeout, ctx.Err())
		}
		return fmt.Errorf("error reading log stream for ImageBuild %s: %w", name, err)
	}

	if streamCompleted {
		GinkgoWriter.Printf("Log stream completed for ImageBuild %s (read %d lines)\n", name, lineCount)
		return nil
	}

	GinkgoWriter.Printf("Log stream closed unexpectedly for ImageBuild %s (read %d lines, no completion marker)\n", name, lineCount)
	return fmt.Errorf("ImageBuild %s: %w", name, ErrLogStreamUnexpectedClose)
}

// handlePlainTextLogStream processes plain text log stream.
// Returns:
//   - nil: stream completed (EOF reached)
//   - ErrLogStreamTimeout: context timeout/cancellation
//   - other error: read error
func (h *Harness) handlePlainTextLogStream(ctx context.Context, name string, body io.Reader) error {
	reader := bufio.NewReader(body)
	lineCount := 0

	for {
		// Check for context cancellation (timeout)
		if ctx.Err() != nil {
			GinkgoWriter.Printf("Log stream timeout for ImageBuild %s (read %d lines)\n", name, lineCount)
			return fmt.Errorf("ImageBuild %s: %w: %w", name, ErrLogStreamTimeout, ctx.Err())
		}

		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			lineCount++
			GinkgoWriter.Printf("[ImageBuild %s] %s", name, line)
		}

		if err != nil {
			if err == io.EOF {
				GinkgoWriter.Printf("Log stream ended for ImageBuild %s (read %d lines)\n", name, lineCount)
				return nil
			}
			if ctx.Err() != nil {
				GinkgoWriter.Printf("Log stream timeout for ImageBuild %s (read %d lines)\n", name, lineCount)
				return fmt.Errorf("ImageBuild %s: %w: %w", name, ErrLogStreamTimeout, ctx.Err())
			}
			return fmt.Errorf("error reading log stream for ImageBuild %s: %w", name, err)
		}
	}
}

// getImageBuildReadyCondition extracts the reason and message from the Ready condition.
func (h *Harness) getImageBuildReadyCondition(imageBuild *imagebuilderapi.ImageBuild) (string, string) {
	if imageBuild.Status == nil || imageBuild.Status.Conditions == nil {
		return "", ""
	}

	for _, cond := range *imageBuild.Status.Conditions {
		if cond.Type == imagebuilderapi.ImageBuildConditionTypeReady {
			return cond.Reason, cond.Message
		}
	}
	return "", ""
}

// WaitForImageBuildWithLogs streams logs for an ImageBuild until completion or timeout.
// Caller should first call WaitForImageBuildProcessing to ensure the build has started.
// Returns the final ImageBuild resource and any error.
func (h *Harness) WaitForImageBuildWithLogs(name string, timeout time.Duration) (*imagebuilderapi.ImageBuild, error) {
	GinkgoWriter.Printf("Streaming logs for ImageBuild %s (timeout: %v)\n", name, timeout)

	// Create a context with timeout for log streaming
	ctx, cancel := context.WithTimeout(h.Context, timeout)
	defer cancel()

	// Start streaming logs with follow=true
	params := &imagebuilderapi.GetImageBuildLogParams{
		Follow: lo.ToPtr(true),
	}

	response, err := h.ImageBuilderClient.GetImageBuildLog(ctx, name, params)
	if err != nil {
		return nil, fmt.Errorf("failed to start log stream for ImageBuild %s: %w", name, err)
	}
	defer response.Body.Close()

	if response.StatusCode != 200 {
		body, _ := io.ReadAll(response.Body)
		return nil, fmt.Errorf("failed to get log stream for ImageBuild %s: status=%d body=%s",
			name, response.StatusCode, string(body))
	}

	// Handle streaming based on content type (matching CLI behavior)
	contentType := response.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") {
		// SSE streaming format
		if err := h.handleSSELogStream(ctx, name, response.Body); err != nil {
			return nil, err
		}
	} else {
		// Plain text format
		if err := h.handlePlainTextLogStream(ctx, name, response.Body); err != nil {
			return nil, err
		}
	}

	// Log stream ended, return the final ImageBuild state
	// Caller is responsible for asserting the final status
	return h.GetImageBuild(name)
}

// GetImageBuildConditionReason returns the current condition reason for an ImageBuild.
func (h *Harness) GetImageBuildConditionReason(name string) (string, error) {
	imageBuild, err := h.GetImageBuild(name)
	if err != nil {
		return "", err
	}

	if imageBuild.Status == nil || imageBuild.Status.Conditions == nil {
		return string(imagebuilderapi.ImageBuildConditionReasonPending), nil
	}

	for _, cond := range *imageBuild.Status.Conditions {
		if cond.Type == imagebuilderapi.ImageBuildConditionTypeReady {
			return cond.Reason, nil
		}
	}

	return string(imagebuilderapi.ImageBuildConditionReasonPending), nil
}

// GetImageBuildConditionReasonAndMessage returns both the reason and message for an ImageBuild condition.
func (h *Harness) GetImageBuildConditionReasonAndMessage(name string) (reason, message string, err error) {
	imageBuild, err := h.GetImageBuild(name)
	if err != nil {
		return "", "", err
	}

	r, m := h.getImageBuildReadyCondition(imageBuild)
	if r == "" {
		return string(imagebuilderapi.ImageBuildConditionReasonPending), "", nil
	}
	return r, m, nil
}

// ImageBuildExists checks if an ImageBuild resource exists.
func (h *Harness) ImageBuildExists(name string) bool {
	_, err := h.GetImageBuild(name)
	return err == nil
}

// ResolveImage resolves an image reference in an OCI registry and returns its descriptor.
// registry: the registry host (e.g., "localhost:5000")
// imageName: the image name (e.g., "myimage" or "namespace/myimage")
// tag: the image tag (e.g., "latest" or "v1.0")
func (h *Harness) ResolveImage(registry, imageName, tag string) (ocispec.Descriptor, error) {
	ref := fmt.Sprintf("%s/%s", registry, imageName)

	repoRef, err := remote.NewRepository(ref)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to create repository reference for %s: %w", ref, err)
	}

	// Use HTTPS with InsecureSkipVerify for e2e registry (may have cert SAN mismatch)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec
	}
	repoRef.Client = &auth.Client{
		Client: &http.Client{
			Transport: transport,
		},
	}

	ctx, cancel := context.WithTimeout(h.Context, 30*time.Second)
	defer cancel()

	desc, err := repoRef.Resolve(ctx, tag)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to resolve image %s:%s: %w", ref, tag, err)
	}

	return desc, nil
}

// NewImageBuildSpec creates a new ImageBuildSpec with the given parameters.
func NewImageBuildSpec(sourceRepo, sourceImage, sourceTag, destRepo, destImage, destTag string, bindingType imagebuilderapi.BindingType) imagebuilderapi.ImageBuildSpec {
	return NewImageBuildSpecWithUserConfig(sourceRepo, sourceImage, sourceTag, destRepo, destImage, destTag, bindingType, "", "")
}

// NewImageBuildSpecWithUserConfig creates a new ImageBuildSpec with the given parameters and optional user configuration.
// If username and publicKey are provided, they will be set as the user configuration for the build.
func NewImageBuildSpecWithUserConfig(sourceRepo, sourceImage, sourceTag, destRepo, destImage, destTag string, bindingType imagebuilderapi.BindingType, username, publicKey string) imagebuilderapi.ImageBuildSpec {
	spec := imagebuilderapi.ImageBuildSpec{
		Source: imagebuilderapi.ImageBuildSource{
			Repository: sourceRepo,
			ImageName:  sourceImage,
			ImageTag:   sourceTag,
		},
		Destination: imagebuilderapi.ImageBuildDestination{
			Repository: destRepo,
			ImageName:  destImage,
			ImageTag:   destTag,
		},
	}

	binding := imagebuilderapi.ImageBuildBinding{}
	switch bindingType {
	case imagebuilderapi.BindingTypeEarly:
		_ = binding.FromEarlyBinding(imagebuilderapi.EarlyBinding{
			Type: imagebuilderapi.Early,
		})
	case imagebuilderapi.BindingTypeLate:
		_ = binding.FromLateBinding(imagebuilderapi.LateBinding{
			Type: imagebuilderapi.Late,
		})
	}
	spec.Binding = binding

	if username != "" && publicKey != "" {
		spec.UserConfiguration = &imagebuilderapi.ImageBuildUserConfiguration{
			Username:  username,
			Publickey: publicKey,
		}
	}

	return spec
}

// ============================================================================
// ImageExport Harness Methods
// ============================================================================

// CreateImageExport creates an ImageExport resource with the given name and spec.
func (h *Harness) CreateImageExport(name string, spec imagebuilderapi.ImageExportSpec) (*imagebuilderapi.ImageExport, error) {
	imageExport := imagebuilderapi.ImageExport{
		ApiVersion: imagebuilderapi.ImageExportAPIVersion,
		Kind:       string(imagebuilderapi.ResourceKindImageExport),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: spec,
	}

	// Add test label to track resources for cleanup
	h.addTestLabelToResource(&imageExport.Metadata)

	response, err := h.ImageBuilderClient.CreateImageExportWithResponse(h.Context, imageExport)
	if err != nil {
		return nil, fmt.Errorf("failed to create ImageExport: %w", err)
	}

	if response.JSON201 == nil {
		return nil, fmt.Errorf("failed to create ImageExport %s: status=%d body=%s",
			name, response.StatusCode(), string(response.Body))
	}

	GinkgoWriter.Printf("Created ImageExport: %s\n", name)
	return response.JSON201, nil
}

// GetImageExport retrieves an ImageExport resource by name.
func (h *Harness) GetImageExport(name string) (*imagebuilderapi.ImageExport, error) {
	response, err := h.ImageBuilderClient.GetImageExportWithResponse(h.Context, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get ImageExport %s: %w", name, err)
	}

	if response.JSON200 == nil {
		return nil, fmt.Errorf("ImageExport %s not found: status=%d body=%s",
			name, response.StatusCode(), string(response.Body))
	}

	return response.JSON200, nil
}

// DeleteImageExport deletes an ImageExport resource by name.
func (h *Harness) DeleteImageExport(name string) error {
	response, err := h.ImageBuilderClient.DeleteImageExportWithResponse(h.Context, name)
	if err != nil {
		return fmt.Errorf("failed to delete ImageExport %s: %w", name, err)
	}

	if response.StatusCode() != 200 && response.StatusCode() != 404 {
		return fmt.Errorf("failed to delete ImageExport %s: status=%d body=%s",
			name, response.StatusCode(), string(response.Body))
	}

	GinkgoWriter.Printf("Deleted ImageExport: %s\n", name)
	return nil
}

// CancelImageExport cancels an in-progress ImageExport.
func (h *Harness) CancelImageExport(name string) error {
	response, err := h.ImageBuilderClient.CancelImageExportWithResponse(h.Context, name)
	if err != nil {
		return fmt.Errorf("failed to cancel ImageExport %s: %w", name, err)
	}

	if response.StatusCode() != 200 {
		return fmt.Errorf("failed to cancel ImageExport %s: status=%d body=%s",
			name, response.StatusCode(), string(response.Body))
	}

	GinkgoWriter.Printf("Canceled ImageExport: %s\n", name)
	return nil
}

// ImageExportExists checks if an ImageExport resource exists.
func (h *Harness) ImageExportExists(name string) bool {
	_, err := h.GetImageExport(name)
	return err == nil
}

// GetImageExportConditionReason returns the current condition reason for an ImageExport.
func (h *Harness) GetImageExportConditionReason(name string) (string, error) {
	imageExport, err := h.GetImageExport(name)
	if err != nil {
		return "", err
	}

	if imageExport.Status == nil || imageExport.Status.Conditions == nil {
		return string(imagebuilderapi.ImageExportConditionReasonPending), nil
	}

	for _, cond := range *imageExport.Status.Conditions {
		if cond.Type == imagebuilderapi.ImageExportConditionTypeReady {
			return cond.Reason, nil
		}
	}

	return string(imagebuilderapi.ImageExportConditionReasonPending), nil
}

// getImageExportReadyCondition extracts the reason and message from the Ready condition.
func (h *Harness) getImageExportReadyCondition(imageExport *imagebuilderapi.ImageExport) (string, string) {
	if imageExport.Status == nil || imageExport.Status.Conditions == nil {
		return "", ""
	}

	for _, cond := range *imageExport.Status.Conditions {
		if cond.Type == imagebuilderapi.ImageExportConditionTypeReady {
			return cond.Reason, cond.Message
		}
	}
	return "", ""
}

// WaitForImageExportProcessing waits for an ImageExport to start processing (leave Pending state).
func (h *Harness) WaitForImageExportProcessing(name string, timeout, polling time.Duration) (*imagebuilderapi.ImageExport, error) {
	GinkgoWriter.Printf("Waiting for ImageExport %s to start processing (timeout: %v, polling: %v)\n", name, timeout, polling)

	var lastReason string
	Eventually(func() (bool, error) {
		imageExport, err := h.GetImageExport(name)
		if err != nil {
			return false, err
		}

		reason, _ := h.getImageExportReadyCondition(imageExport)
		lastReason = reason

		// Export has started processing if it's no longer Pending
		if reason != "" && reason != string(imagebuilderapi.ImageExportConditionReasonPending) {
			GinkgoWriter.Printf("ImageExport %s started processing: reason=%s\n", name, reason)
			return true, nil
		}

		GinkgoWriter.Printf("ImageExport %s still pending...\n", name)
		return false, nil
	}, timeout, polling).Should(BeTrue(),
		fmt.Sprintf("Expected ImageExport %s to start processing, but still in state: %s", name, lastReason))

	return h.GetImageExport(name)
}

// WaitForImageExportWithLogs streams logs for an ImageExport until completion or timeout.
func (h *Harness) WaitForImageExportWithLogs(name string, timeout time.Duration) (*imagebuilderapi.ImageExport, error) {
	GinkgoWriter.Printf("Streaming logs for ImageExport %s (timeout: %v)\n", name, timeout)

	// Create a context with timeout for log streaming
	ctx, cancel := context.WithTimeout(h.Context, timeout)
	defer cancel()

	// Start streaming logs with follow=true
	params := &imagebuilderapi.GetImageExportLogParams{
		Follow: lo.ToPtr(true),
	}

	response, err := h.ImageBuilderClient.GetImageExportLog(ctx, name, params)
	if err != nil {
		return nil, fmt.Errorf("failed to start log stream for ImageExport %s: %w", name, err)
	}
	defer response.Body.Close()

	if response.StatusCode != 200 {
		body, _ := io.ReadAll(response.Body)
		return nil, fmt.Errorf("failed to get log stream for ImageExport %s: status=%d body=%s",
			name, response.StatusCode, string(body))
	}

	// Handle streaming based on content type (matching CLI behavior)
	contentType := response.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") {
		// SSE streaming format - reuse the ImageBuild SSE handler with ImageExport prefix
		if err := h.handleImageExportSSELogStream(ctx, name, response.Body); err != nil {
			return nil, err
		}
	} else {
		// Plain text format
		if err := h.handleImageExportPlainTextLogStream(ctx, name, response.Body); err != nil {
			return nil, err
		}
	}

	// Log stream ended, return the final ImageExport state
	return h.GetImageExport(name)
}

// handleImageExportSSELogStream processes Server-Sent Events log stream for ImageExport.
func (h *Harness) handleImageExportSSELogStream(ctx context.Context, name string, body io.Reader) error {
	scanner := bufio.NewScanner(body)
	const maxScannerBuffer = 1024 * 1024 // 1MB
	scanner.Buffer(make([]byte, 0, maxScannerBuffer), maxScannerBuffer)

	var currentData strings.Builder
	lineCount := 0
	streamCompleted := false

	for scanner.Scan() {
		if ctx.Err() != nil {
			GinkgoWriter.Printf("Log stream timeout for ImageExport %s (read %d lines)\n", name, lineCount)
			return fmt.Errorf("ImageExport %s: %w: %w", name, ErrLogStreamTimeout, ctx.Err())
		}

		line := scanner.Text()

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == imagebuilderapi.LogStreamCompleteMarker {
				streamCompleted = true
				break
			}
			currentData.WriteString(data)
		} else if line == "" {
			if currentData.Len() > 0 {
				lineCount++
				data := currentData.String()
				if !strings.HasSuffix(data, "\n") {
					data += "\n"
				}
				GinkgoWriter.Printf("[ImageExport %s] %s", name, data)
				currentData.Reset()
			}
		} else {
			lineCount++
			GinkgoWriter.Printf("[ImageExport %s] %s\n", name, line)
		}
	}

	if currentData.Len() > 0 {
		lineCount++
		data := currentData.String()
		if !strings.HasSuffix(data, "\n") {
			data += "\n"
		}
		GinkgoWriter.Printf("[ImageExport %s] %s", name, data)
	}

	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			GinkgoWriter.Printf("Log stream timeout for ImageExport %s (read %d lines)\n", name, lineCount)
			return fmt.Errorf("ImageExport %s: %w: %w", name, ErrLogStreamTimeout, ctx.Err())
		}
		return fmt.Errorf("error reading log stream for ImageExport %s: %w", name, err)
	}

	if streamCompleted {
		GinkgoWriter.Printf("Log stream completed for ImageExport %s (read %d lines)\n", name, lineCount)
		return nil
	}

	GinkgoWriter.Printf("Log stream closed unexpectedly for ImageExport %s (read %d lines, no completion marker)\n", name, lineCount)
	return fmt.Errorf("ImageExport %s: %w", name, ErrLogStreamUnexpectedClose)
}

// handleImageExportPlainTextLogStream processes plain text log stream for ImageExport.
func (h *Harness) handleImageExportPlainTextLogStream(ctx context.Context, name string, body io.Reader) error {
	reader := bufio.NewReader(body)
	lineCount := 0

	for {
		if ctx.Err() != nil {
			GinkgoWriter.Printf("Log stream timeout for ImageExport %s (read %d lines)\n", name, lineCount)
			return fmt.Errorf("ImageExport %s: %w: %w", name, ErrLogStreamTimeout, ctx.Err())
		}

		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			lineCount++
			GinkgoWriter.Printf("[ImageExport %s] %s", name, line)
		}

		if err != nil {
			if err == io.EOF {
				GinkgoWriter.Printf("Log stream ended for ImageExport %s (read %d lines)\n", name, lineCount)
				return nil
			}
			if ctx.Err() != nil {
				GinkgoWriter.Printf("Log stream timeout for ImageExport %s (read %d lines)\n", name, lineCount)
				return fmt.Errorf("ImageExport %s: %w: %w", name, ErrLogStreamTimeout, ctx.Err())
			}
			return fmt.Errorf("error reading log stream for ImageExport %s: %w", name, err)
		}
	}
}

// DownloadImageExport downloads an ImageExport artifact and returns the response body.
// The caller is responsible for closing the returned io.ReadCloser.
func (h *Harness) DownloadImageExport(name string) (io.ReadCloser, int64, error) {
	response, err := h.ImageBuilderClient.DownloadImageExport(h.Context, name)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to download ImageExport %s: %w", name, err)
	}

	if response.StatusCode != 200 {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		return nil, 0, fmt.Errorf("failed to download ImageExport %s: status=%d body=%s",
			name, response.StatusCode, string(body))
	}

	contentLength := response.ContentLength
	GinkgoWriter.Printf("Downloading ImageExport %s (Content-Length: %d bytes)\n", name, contentLength)

	return response.Body, contentLength, nil
}

// NewImageExportSpec creates a new ImageExportSpec from an ImageBuild reference.
func NewImageExportSpec(imageBuildName string, format imagebuilderapi.ExportFormatType) imagebuilderapi.ImageExportSpec {
	source := imagebuilderapi.ImageExportSource{}
	// Type is automatically set by FromImageBuildRefSource
	_ = source.FromImageBuildRefSource(imagebuilderapi.ImageBuildRefSource{
		ImageBuildRef: imageBuildName,
	})

	return imagebuilderapi.ImageExportSpec{
		Source: source,
		Format: format,
	}
}

// GenerateEnrollmentConfig generates enrollment certificates and agent config for late binding.
// Uses CLI to create the CSR (which handles key generation and certificate retrieval).
// The csrName parameter is used to name the certificate signing request (must be unique per test).
// Returns the agent config YAML content with embedded credentials.
func (h *Harness) GenerateEnrollmentConfig(csrName string) (string, error) {
	// Create a temp directory for the certificate files
	tempDir, err := os.MkdirTemp("", "enrollment-config-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Generate a unique CSR name to avoid conflicts
	uniqueCSRName := fmt.Sprintf("%s-%s", csrName, h.GetTestIDFromContext()[:8])

	// Delete any existing CSR with similar name (ignore errors)
	_, _ = h.Client.DeleteCertificateSigningRequestWithResponse(h.Context, uniqueCSRName)

	// Use CLI to request certificate - this handles key generation, CSR creation, and cert retrieval
	GinkgoWriter.Printf("Requesting enrollment certificate via CLI for CSR %s\n", uniqueCSRName)
	configOutput, err := h.CLI("certificate", "request", "-n", uniqueCSRName, "-d", tempDir, "-o", "embedded")
	if err != nil {
		return "", fmt.Errorf("failed to request certificate: %w", err)
	}

	// Add agent configuration settings for faster polling in tests
	fullConfig := configOutput + `
spec-fetch-interval: 0m5s
status-update-interval: 0m5s
`

	GinkgoWriter.Printf("Generated enrollment config for CSR %s\n", uniqueCSRName)
	return fullConfig, nil
}

// CreateCloudInitDir creates a cloud-init directory with user-data and meta-data files.
// The agentConfig is base64-encoded and written to /etc/flightctl/config.yaml on the VM.
// Returns the path to the cloud-init directory.
func (h *Harness) CreateCloudInitDir(tempDir, vmName, agentConfig string) (string, error) {
	cloudInitDir := filepath.Join(tempDir, "cloud-init")
	if err := os.MkdirAll(cloudInitDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create cloud-init directory: %w", err)
	}

	// Create meta-data file
	metaData := fmt.Sprintf(`instance-id: %s
local-hostname: %s
`, vmName, vmName)

	metaDataPath := filepath.Join(cloudInitDir, "meta-data")
	if err := os.WriteFile(metaDataPath, []byte(metaData), 0o600); err != nil {
		return "", fmt.Errorf("failed to write meta-data: %w", err)
	}

	// Base64 encode the agent config
	agentConfigB64 := base64.StdEncoding.EncodeToString([]byte(agentConfig))

	// Create user-data file with cloud-config
	userData := fmt.Sprintf(`#cloud-config
write_files:
- path: /etc/flightctl/config.yaml
  content: %s
  encoding: b64
  permissions: '0644'
- path: /etc/motd
  content: |
    Welcome to Flight Control Device
    This device is managed by Flight Control - Late binding!
  owner: root:root
  permissions: '0644'
`, agentConfigB64)

	userDataPath := filepath.Join(cloudInitDir, "user-data")
	if err := os.WriteFile(userDataPath, []byte(userData), 0o600); err != nil {
		return "", fmt.Errorf("failed to write user-data: %w", err)
	}

	GinkgoWriter.Printf("Created cloud-init directory at %s\n", cloudInitDir)
	return cloudInitDir, nil
}

const imageBuilderWorkerLabelSelector = "flightctl.service=flightctl-imagebuilder-worker"

// getImageBuilderWorkerPods returns the imagebuilder-worker pods and their namespace.
// If no pods are found, it returns an empty list and the detected namespace.
func (h *Harness) getImageBuilderWorkerPods() ([]corev1.Pod, string, error) {
	// Try to find pods across all namespaces first
	pods, err := h.Cluster.CoreV1().Pods("").List(h.Context, metav1.ListOptions{
		LabelSelector: imageBuilderWorkerLabelSelector,
	})
	if err != nil {
		return nil, "", fmt.Errorf("failed to list imagebuilder-worker pods: %w", err)
	}

	if len(pods.Items) > 0 {
		namespace := pods.Items[0].Namespace
		return pods.Items, namespace, nil
	}

	// No pods found, try to detect namespace from deployment
	for _, ns := range []string{"flightctl-internal", "flightctl", "default"} {
		_, err := h.Cluster.AppsV1().Deployments(ns).Get(h.Context, "flightctl-imagebuilder-worker", metav1.GetOptions{})
		if err == nil {
			return nil, ns, nil
		}
	}

	// Default fallback
	return nil, "flightctl-internal", nil
}

// KillImageBuilderWorkerPods kills all imagebuilder-worker pods.
// This is used to test recovery scenarios.
func (h *Harness) KillImageBuilderWorkerPods() error {
	pods, namespace, err := h.getImageBuilderWorkerPods()
	if err != nil {
		return err
	}

	GinkgoWriter.Printf("Killing imagebuilder-worker pods in namespace %s\n", namespace)

	if len(pods) == 0 {
		GinkgoWriter.Printf("No imagebuilder-worker pods found to kill\n")
		return nil
	}

	// Delete each pod with grace period 0 (force delete)
	gracePeriod := int64(0)
	deleteOptions := metav1.DeleteOptions{
		GracePeriodSeconds: &gracePeriod,
	}

	for _, pod := range pods {
		err := h.Cluster.CoreV1().Pods(namespace).Delete(h.Context, pod.Name, deleteOptions)
		if err != nil {
			GinkgoWriter.Printf("Failed to delete pod %s: %v\n", pod.Name, err)
		} else {
			GinkgoWriter.Printf("Deleted pod: %s\n", pod.Name)
		}
	}

	GinkgoWriter.Printf("Killed %d imagebuilder-worker pods\n", len(pods))
	return nil
}

// UpdateImageBuilderWorkerConfig updates the imagebuilder-worker config using the provided
// modifier function and restarts the worker pods to apply the change.
func (h *Harness) UpdateImageBuilderWorkerConfig(modifier func(*config.Config)) error {
	_, namespace, err := h.getImageBuilderWorkerPods()
	if err != nil {
		return err
	}
	configMapName := "flightctl-imagebuilder-worker-config"

	GinkgoWriter.Printf("Updating imagebuilder-worker config in configmap %s/%s\n", namespace, configMapName)

	// Get current configmap using Kubernetes client
	cm, err := h.Cluster.CoreV1().ConfigMaps(namespace).Get(h.Context, configMapName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get configmap: %w", err)
	}

	// Get the config.yaml data
	configYAML, ok := cm.Data["config.yaml"]
	if !ok {
		return fmt.Errorf("config.yaml not found in configmap %s", configMapName)
	}

	// Print config before change
	GinkgoWriter.Printf("Config BEFORE change:\n%s\n", configYAML)

	// Parse the YAML config using the shared config type
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
		return fmt.Errorf("failed to parse config YAML: %w", err)
	}

	// Initialize ImageBuilderWorker if nil
	if cfg.ImageBuilderWorker == nil {
		cfg.ImageBuilderWorker = config.NewDefaultImageBuilderWorkerConfig()
	}

	// Apply the modifier function
	modifier(&cfg)

	// Marshal back to YAML
	updatedConfig, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal updated config: %w", err)
	}

	// Print config after change
	GinkgoWriter.Printf("Config AFTER change:\n%s\n", string(updatedConfig))

	// Update the configmap
	cm.Data["config.yaml"] = string(updatedConfig)
	_, err = h.Cluster.CoreV1().ConfigMaps(namespace).Update(h.Context, cm, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update configmap: %w", err)
	}

	GinkgoWriter.Printf("Updated configmap %s/%s\n", namespace, configMapName)

	// Restart the worker pods to pick up the new config
	if err := h.KillImageBuilderWorkerPods(); err != nil {
		return fmt.Errorf("failed to restart worker pods: %w", err)
	}

	// Wait for pods to be ready again
	if err := h.WaitForImageBuilderWorkerReady(2 * time.Minute); err != nil {
		return fmt.Errorf("failed waiting for worker pods to be ready: %w", err)
	}

	GinkgoWriter.Printf("Successfully updated imagebuilder-worker config\n")
	return nil
}

// SetMaxConcurrentBuilds updates the maxConcurrentBuilds setting in the worker config
// and restarts the worker pods to apply the change.
func (h *Harness) SetMaxConcurrentBuilds(maxConcurrentBuilds int) error {
	GinkgoWriter.Printf("Setting maxConcurrentBuilds to %d\n", maxConcurrentBuilds)
	return h.UpdateImageBuilderWorkerConfig(func(cfg *config.Config) {
		cfg.ImageBuilderWorker.MaxConcurrentBuilds = maxConcurrentBuilds
	})
}

// WaitForImageBuilderWorkerReady waits for the imagebuilder-worker pods to be ready.
func (h *Harness) WaitForImageBuilderWorkerReady(timeout time.Duration) error {
	GinkgoWriter.Printf("Waiting for imagebuilder-worker pods to be ready (timeout: %v)\n", timeout)

	Eventually(func() (bool, error) {
		pods, _, err := h.getImageBuilderWorkerPods()
		if err != nil {
			GinkgoWriter.Printf("Error listing pods: %v\n", err)
			return false, nil
		}

		if len(pods) == 0 {
			GinkgoWriter.Printf("No pods found yet\n")
			return false, nil
		}

		// Check if all pods are ready
		allReady := true
		for _, pod := range pods {
			podReady := false
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
					podReady = true
					break
				}
			}
			if !podReady {
				allReady = false
				break
			}
		}

		if allReady {
			GinkgoWriter.Printf("All imagebuilder-worker pods are ready (%d pods)\n", len(pods))
		} else {
			GinkgoWriter.Printf("Imagebuilder-worker pods not ready yet (%d pods)\n", len(pods))
		}

		return allReady, nil
	}, timeout, 5*time.Second).Should(BeTrue(),
		"Imagebuilder-worker pods should become ready")

	return nil
}
