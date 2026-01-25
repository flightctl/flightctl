package tasks

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// imageExportStatusUpdateRequest represents a request to update the ImageExport status
type imageExportStatusUpdateRequest struct {
	Condition      *api.ImageExportCondition
	LastSeen       *time.Time
	ManifestDigest *string
}

// newImageExportStatusUpdateRequest creates a new update request with LastSeen automatically set to now
func newImageExportStatusUpdateRequest() imageExportStatusUpdateRequest {
	now := time.Now().UTC()
	return imageExportStatusUpdateRequest{
		LastSeen: &now,
	}
}

// imageExportStatusUpdater manages all status updates for an ImageExport, ensuring atomic updates
// and preventing race conditions between LastSeen and condition updates.
type imageExportStatusUpdater struct {
	imageExportService imagebuilderapi.ImageExportService
	orgID              uuid.UUID
	imageExportName    string
	kvStore            kvstore.KVStore
	updateChan         chan imageExportStatusUpdateRequest
	outputChan         chan []byte // Central channel for all task outputs
	ctx                context.Context
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
	log                logrus.FieldLogger
	// logBuffer keeps the last 500 lines of logs in memory
	logBuffer   []string
	logBufferMu sync.Mutex
}

// startImageExportStatusUpdater starts a goroutine that is the single writer for ImageExport status updates.
func startImageExportStatusUpdater(
	ctx context.Context,
	imageExportService imagebuilderapi.ImageExportService,
	orgID uuid.UUID,
	imageExportName string,
	kvStore kvstore.KVStore,
	cfg *config.Config,
	log logrus.FieldLogger,
) (*imageExportStatusUpdater, func()) {
	updaterCtx, updaterCancel := context.WithCancel(ctx)

	updater := &imageExportStatusUpdater{
		imageExportService: imageExportService,
		orgID:              orgID,
		imageExportName:    imageExportName,
		kvStore:            kvStore,
		updateChan:         make(chan imageExportStatusUpdateRequest),
		outputChan:         make(chan []byte, 100),
		ctx:                updaterCtx,
		cancel:             updaterCancel,
		log:                log,
		logBuffer:          make([]string, 0, 500),
	}

	updater.wg.Add(1)
	go updater.run(cfg)

	cleanup := func() {
		updaterCancel()
		updater.wg.Wait()
		close(updater.updateChan)
		close(updater.outputChan)
	}

	return updater, cleanup
}

// run is the main loop for the status updater goroutine
func (u *imageExportStatusUpdater) run(cfg *config.Config) {
	defer u.wg.Done()

	if cfg == nil || cfg.ImageBuilderWorker == nil {
		u.log.Error("Config or ImageBuilderWorker config is nil, cannot update status")
		return
	}
	updateInterval := time.Duration(cfg.ImageBuilderWorker.LastSeenUpdateInterval)
	ticker := time.NewTicker(updateInterval)
	defer ticker.Stop()

	var pendingCondition *api.ImageExportCondition
	lastSeenUpdateTime := time.Now().UTC()

	var lastOutputTime *time.Time
	var lastSetLastSeen *time.Time

	for {
		select {
		case <-u.ctx.Done():
			return
		case <-ticker.C:
			if lastOutputTime != nil {
				if lastSetLastSeen == nil || !lastOutputTime.Equal(*lastSetLastSeen) {
					lastSeenUpdateTime = *lastOutputTime
					lastSetLastSeenCopy := *lastOutputTime
					lastSetLastSeen = &lastSetLastSeenCopy
					u.updateStatus(u.ctx, pendingCondition, &lastSeenUpdateTime, nil)
					// Also persist logs to DB periodically
					u.persistLogsToDB(u.ctx)
					pendingCondition = nil
				}
			}
		case output := <-u.outputChan:
			now := time.Now().UTC()
			lastOutputTime = &now
			u.log.Debugf("Task output: %s", string(output))
			// Write to Redis and update in-memory buffer
			u.writeLogToRedis(u.ctx, output)
		case req := <-u.updateChan:
			if req.Condition != nil {
				pendingCondition = req.Condition
			}
			if req.LastSeen != nil {
				lastSeenUpdateTime = *req.LastSeen
			}
			// Update immediately when condition or manifest digest changes
			// LastSeen-only updates are handled immediately to set initial value
			if req.Condition != nil || req.ManifestDigest != nil || req.LastSeen != nil {
				u.updateStatus(u.ctx, pendingCondition, &lastSeenUpdateTime, req.ManifestDigest)
				// If condition is completed or failed, persist logs to DB and signal stream completion
				if pendingCondition != nil {
					if pendingCondition.Reason == string(api.ImageExportConditionReasonCompleted) ||
						pendingCondition.Reason == string(api.ImageExportConditionReasonFailed) {
						u.persistLogsToDB(u.ctx)
						u.writeStreamCompleteMarker(u.ctx)
					}
				}
				pendingCondition = nil
			}
		}
	}
}

// updateStatus performs the actual database update
func (u *imageExportStatusUpdater) updateStatus(ctx context.Context, condition *api.ImageExportCondition, lastSeen *time.Time, manifestDigest *string) {
	imageExport, status := u.imageExportService.Get(ctx, u.orgID, u.imageExportName)
	if imageExport == nil || !imagebuilderapi.IsStatusOK(status) {
		u.log.WithField("status", status).Warn("Failed to load ImageExport for status update")
		return
	}

	if imageExport.Status == nil {
		imageExport.Status = &api.ImageExportStatus{}
	}

	if lastSeen != nil {
		imageExport.Status.LastSeen = lastSeen
	}

	if condition != nil {
		if imageExport.Status.Conditions == nil {
			imageExport.Status.Conditions = &[]api.ImageExportCondition{}
		}
		api.SetImageExportStatusCondition(imageExport.Status.Conditions, *condition)
	}

	if manifestDigest != nil {
		imageExport.Status.ManifestDigest = manifestDigest
	}

	_, err := u.imageExportService.UpdateStatus(ctx, u.orgID, imageExport)
	if err != nil {
		u.log.WithError(err).Warn("Failed to update ImageExport status")
	}
}

// updateCondition sends a condition update request to the updater goroutine
func (u *imageExportStatusUpdater) updateCondition(condition api.ImageExportCondition) {
	req := newImageExportStatusUpdateRequest()
	req.Condition = &condition
	select {
	case u.updateChan <- req:
	case <-u.ctx.Done():
	}
}

// setManifestDigest sets the manifest digest in the ImageExport status
func (u *imageExportStatusUpdater) setManifestDigest(manifestDigest string) {
	req := newImageExportStatusUpdateRequest()
	req.ManifestDigest = &manifestDigest
	select {
	case u.updateChan <- req:
	case <-u.ctx.Done():
	}
}

// reportOutput sends task output to the central output handler
func (u *imageExportStatusUpdater) reportOutput(output []byte) {
	select {
	case u.outputChan <- output:
	case <-u.ctx.Done():
	}
}

// getLogKey returns the Redis key for this ImageExport's logs
func (u *imageExportStatusUpdater) getLogKey() string {
	return fmt.Sprintf("imageexport:logs:%s:%s", u.orgID.String(), u.imageExportName)
}

// writeLogToRedis writes log output to Redis and updates the in-memory buffer
// The in-memory buffer keeps only the last 500 lines to save memory
func (u *imageExportStatusUpdater) writeLogToRedis(ctx context.Context, output []byte) {
	if u.kvStore == nil {
		return
	}

	key := u.getLogKey()

	// Write full logs to Redis stream
	_, err := u.kvStore.StreamAdd(ctx, key, output)
	if err != nil {
		u.log.WithError(err).Warn("Failed to write log to Redis")
		return
	}

	// Set TTL on the key (1 hour)
	if err := u.kvStore.SetExpire(ctx, key, 1*time.Hour); err != nil {
		u.log.WithError(err).Warn("Failed to set TTL on Redis log key")
	}

	// Update in-memory buffer (keep only last 500 lines)
	u.logBufferMu.Lock()
	defer u.logBufferMu.Unlock()

	// Split output into lines and add to buffer
	outputStr := string(output)
	lines := strings.Split(outputStr, "\n")
	// Remove empty last line if output ends with newline
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	// Add lines to buffer
	u.logBuffer = append(u.logBuffer, lines...)

	// Keep only last 500 lines
	const maxLines = 500
	if len(u.logBuffer) > maxLines {
		u.logBuffer = u.logBuffer[len(u.logBuffer)-maxLines:]
	}
}

// persistLogsToDB persists the last 500 lines of logs to the database
func (u *imageExportStatusUpdater) persistLogsToDB(ctx context.Context) {
	u.logBufferMu.Lock()
	defer u.logBufferMu.Unlock()

	// Get the last 500 lines from buffer
	logs := strings.Join(u.logBuffer, "\n")
	if logs != "" && !strings.HasSuffix(logs, "\n") {
		logs += "\n"
	}

	// Persist to DB using the service's UpdateLogs method
	if logs != "" {
		u.log.Debugf("Persisting %d lines of logs to DB", len(u.logBuffer))
		if err := u.imageExportService.UpdateLogs(ctx, u.orgID, u.imageExportName, logs); err != nil {
			u.log.WithError(err).Warn("Failed to persist logs to DB")
		}
	}
}

// writeStreamCompleteMarker writes a completion marker to Redis to signal that log streaming is complete
// This allows clients following the log stream to know the stream has ended
func (u *imageExportStatusUpdater) writeStreamCompleteMarker(ctx context.Context) {
	if u.kvStore == nil {
		return
	}

	key := u.getLogKey()
	_, err := u.kvStore.StreamAdd(ctx, key, []byte(api.LogStreamCompleteMarker))
	if err != nil {
		u.log.WithError(err).Warn("Failed to write stream complete marker to Redis")
	}
}
