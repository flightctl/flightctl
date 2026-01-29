package e2e

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	imagebuilderapi "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
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
	imageBuild := imagebuilderapi.ImageBuild{
		ApiVersion: imagebuilderapi.ImageBuildAPIVersion,
		Kind:       string(imagebuilderapi.ResourceKindImageBuild),
		Metadata: v1beta1.ObjectMeta{
			Name: lo.ToPtr(name),
		},
		Spec: spec,
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

	return spec
}
