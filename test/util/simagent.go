package util

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent"
	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/lifecycle"
	apiClient "github.com/flightctl/flightctl/internal/api/client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"k8s.io/apimachinery/pkg/util/wait"
)

// NewSimulatedAgent finalizes cfg (Complete + Validate) and constructs the corresponding agent.Agent
// with a prefixed logger. This consolidates the setup sequence previously duplicated between
// devicesimulator and integration test harnesses.
func NewSimulatedAgent(cfg *agent_config.Config, name string, opts ...agent.AgentOption) (*agent.Agent, error) {
	if err := cfg.Complete(); err != nil {
		return nil, fmt.Errorf("completing agent config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating agent config: %w", err)
	}
	logWithPrefix := flightlog.NewPrefixLogger(name)
	if cfg.LogLevel != "" {
		logWithPrefix.Level(cfg.LogLevel)
	}
	return agent.New(logWithPrefix, cfg, "", opts...), nil
}

// ReadBannerFile reads the enrollment banner file written by the agent's lifecycle manager under agentDir.
func ReadBannerFile(agentDir string) (string, error) {
	bannerFile := filepath.Join(agentDir, lifecycle.BannerFile)
	if _, err := os.Stat(bannerFile); err != nil {
		return "", err
	}
	data, err := os.ReadFile(bannerFile)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WaitForEnrollmentID polls agentDir's banner file until an enrollment ID appears, the context is
// cancelled, or timeout elapses. Returns the discovered enrollment ID (empty if never found) and
// the poll error, if any.
func WaitForEnrollmentID(ctx context.Context, agentDir string, pollInterval, timeout time.Duration) (string, error) {
	enrollmentID := ""
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		bannerFileData, err := ReadBannerFile(agentDir)
		if err != nil {
			return false, nil
		}
		enrollmentID = GetEnrollmentIdFromText(bannerFileData)
		return enrollmentID != "", nil
	})
	return enrollmentID, err
}

// ApproveEnrollment polls agentDir's banner file for the enrollment ID and repeatedly attempts to
// approve it via serviceClient until it succeeds, the context is cancelled, or timeout elapses.
// Returns the discovered enrollment ID (empty if never found) and a terminal error (nil on success).
func ApproveEnrollment(ctx context.Context, serviceClient *apiClient.ClientWithResponses, agentDir string, labels *map[string]string, pollInterval, timeout time.Duration) (string, error) {
	enrollmentID := ""
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		if enrollmentID == "" {
			bannerFileData, err := ReadBannerFile(agentDir)
			if err != nil {
				return false, nil
			}
			enrollmentID = GetEnrollmentIdFromText(bannerFileData)
			if enrollmentID == "" {
				return false, nil
			}
		}
		approveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		resp, err := serviceClient.ApproveEnrollmentRequestWithResponse(
			approveCtx,
			enrollmentID,
			v1beta1.EnrollmentRequestApproval{
				Approved: true,
				Labels:   labels,
			})
		if err != nil {
			return false, nil
		}
		code := resp.StatusCode()
		if code == http.StatusNotFound {
			// no error, but don't treat as exceptional: there can be a race between posting and approving
			return false, nil
		}
		if code < http.StatusOK || code >= http.StatusMultipleChoices {
			return false, nil
		}
		return true, nil
	})
	return enrollmentID, err
}
