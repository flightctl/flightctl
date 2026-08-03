package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	apiClient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/sirupsen/logrus"
)

const (
	simulatorCreatedByLabel = "created_by=device-simulator"
	simulatorCreatedByValue = "device-simulator"
	cleanupProgressEveryN   = 500
	cleanupProgressInterval = 5 * time.Second
	cleanupListLimit        = int32(500)
)

type cleanupProgress struct {
	log       *logrus.Logger
	kind      string
	deleted   int
	remaining *int64
	lastLog   time.Time
}

func (p *cleanupProgress) maybeLog() {
	if p.deleted%cleanupProgressEveryN != 0 && time.Since(p.lastLog) < cleanupProgressInterval {
		return
	}
	if p.remaining != nil {
		p.log.Infof("deleted %d %s so far (%d remaining)", p.deleted, p.kind, *p.remaining)
	} else {
		p.log.Infof("deleted %d %s so far", p.deleted, p.kind)
	}
	p.lastLog = time.Now()
}

func cleanSimulatorState(ctx context.Context, log *logrus.Logger, serviceClient *apiClient.ClientWithResponses, dataDir string) error {
	start := time.Now()
	log.Infoln("cleaning simulator-created resources")

	localRemoved, err := wipeLocalAgentDirs(log, dataDir)
	if err != nil {
		return fmt.Errorf("wiping local agent dirs: %w", err)
	}

	deviceNames, devicesDeleted, err := deleteLabeledDevices(ctx, log, serviceClient)
	if err != nil {
		return fmt.Errorf("deleting simulator devices: %w", err)
	}

	matchedERs, err := deleteEnrollmentRequestsByName(ctx, log, serviceClient, deviceNames)
	if err != nil {
		return fmt.Errorf("deleting enrollment requests by device name: %w", err)
	}

	pendingERs, err := deletePendingSimulatorEnrollmentRequests(ctx, log, serviceClient)
	if err != nil {
		return fmt.Errorf("deleting pending simulator enrollment requests: %w", err)
	}

	log.Infof("cleanup complete in %s: removed %d local dirs, deleted %d devices, %d matched ERs, %d pending simulator ERs",
		time.Since(start).Round(time.Millisecond), localRemoved, devicesDeleted, matchedERs, pendingERs)
	return nil
}

func wipeLocalAgentDirs(log *logrus.Logger, dataDir string) (int, error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		path := filepath.Join(dataDir, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return removed, fmt.Errorf("removing %s: %w", path, err)
		}
		removed++
	}
	log.Infof("removed %d local entries under %s", removed, dataDir)
	return removed, nil
}

func deleteLabeledDevices(ctx context.Context, log *logrus.Logger, serviceClient *apiClient.ClientWithResponses) ([]string, int, error) {
	selector := simulatorCreatedByLabel
	limit := cleanupListLimit
	var continueToken *string
	names := make([]string, 0)
	progress := &cleanupProgress{log: log, kind: "devices", lastLog: time.Now()}

	for {
		resp, err := serviceClient.ListDevicesWithResponse(ctx, &v1beta1.ListDevicesParams{
			LabelSelector: &selector,
			Limit:         &limit,
			Continue:      continueToken,
		})
		if err != nil {
			return nil, progress.deleted, err
		}
		if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
			return nil, progress.deleted, fmt.Errorf("list devices: status %d", resp.StatusCode())
		}

		progress.remaining = resp.JSON200.Metadata.RemainingItemCount
		for _, device := range resp.JSON200.Items {
			if device.Metadata.Name == nil {
				continue
			}
			name := *device.Metadata.Name
			delResp, err := serviceClient.DeleteDeviceWithResponse(ctx, name)
			if err != nil {
				return names, progress.deleted, fmt.Errorf("delete device %s: %w", name, err)
			}
			code := delResp.StatusCode()
			if code != http.StatusOK && code != http.StatusNoContent && code != http.StatusNotFound {
				return names, progress.deleted, fmt.Errorf("delete device %s: status %d", name, code)
			}
			if code != http.StatusNotFound {
				names = append(names, name)
				progress.deleted++
				progress.maybeLog()
			}
		}

		if resp.JSON200.Metadata.Continue == nil || *resp.JSON200.Metadata.Continue == "" {
			break
		}
		continueToken = resp.JSON200.Metadata.Continue
	}
	return names, progress.deleted, nil
}

func deleteEnrollmentRequestsByName(ctx context.Context, log *logrus.Logger, serviceClient *apiClient.ClientWithResponses, names []string) (int, error) {
	progress := &cleanupProgress{log: log, kind: "matched enrollment requests", lastLog: time.Now()}
	for _, name := range names {
		delResp, err := serviceClient.DeleteEnrollmentRequestWithResponse(ctx, name)
		if err != nil {
			log.Warnf("delete enrollment request %s: %v", name, err)
			continue
		}
		code := delResp.StatusCode()
		if code == http.StatusNotFound {
			continue
		}
		if code != http.StatusOK && code != http.StatusNoContent {
			log.Warnf("delete enrollment request %s: status %d", name, code)
			continue
		}
		progress.deleted++
		progress.maybeLog()
	}
	return progress.deleted, nil
}

func deletePendingSimulatorEnrollmentRequests(ctx context.Context, log *logrus.Logger, serviceClient *apiClient.ClientWithResponses) (int, error) {
	fieldSelector := "status.approval.approved!=true"
	limit := cleanupListLimit
	var continueToken *string
	progress := &cleanupProgress{log: log, kind: "pending simulator enrollment requests", lastLog: time.Now()}

	for {
		resp, err := serviceClient.ListEnrollmentRequestsWithResponse(ctx, &v1beta1.ListEnrollmentRequestsParams{
			FieldSelector: &fieldSelector,
			Limit:         &limit,
			Continue:      continueToken,
		})
		if err != nil {
			return progress.deleted, err
		}
		if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
			return progress.deleted, fmt.Errorf("list enrollment requests: status %d", resp.StatusCode())
		}

		progress.remaining = resp.JSON200.Metadata.RemainingItemCount
		for _, er := range resp.JSON200.Items {
			if er.Metadata.Name == nil || er.Spec.Labels == nil {
				continue
			}
			if (*er.Spec.Labels)["created_by"] != simulatorCreatedByValue {
				continue
			}
			name := *er.Metadata.Name
			delResp, err := serviceClient.DeleteEnrollmentRequestWithResponse(ctx, name)
			if err != nil {
				return progress.deleted, fmt.Errorf("delete enrollment request %s: %w", name, err)
			}
			code := delResp.StatusCode()
			if code == http.StatusNotFound {
				continue
			}
			if code != http.StatusOK && code != http.StatusNoContent {
				return progress.deleted, fmt.Errorf("delete enrollment request %s: status %d", name, code)
			}
			progress.deleted++
			progress.maybeLog()
		}

		if resp.JSON200.Metadata.Continue == nil || *resp.JSON200.Metadata.Continue == "" {
			break
		}
		continueToken = resp.JSON200.Metadata.Continue
	}
	return progress.deleted, nil
}
