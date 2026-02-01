package tasks

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// imageExportStatusUpdateRequest represents a request to update the ImageExport status
type imageExportStatusUpdateRequest struct {
	Condition      *domain.ImageExportCondition
	LastSeen       *time.Time
	ManifestDigest *string
	// done is closed when the update has been processed (used for terminal conditions)
	done chan struct{}
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
	cancelExport       func() // Cancel function for the export context
	wg                 sync.WaitGroup
	log                logrus.FieldLogger
	// logBuffer keeps the last 500 lines of logs in memory
	logBuffer   []string
	logBufferMu sync.Mutex
}

// startImageExportStatusUpdater starts a goroutine that is the single writer for ImageExport status updates.
// ctx is the parent context (consumer context) - NOT the export context.
// cancelExport is the cancel function for the export context, called when cancellation is received.
func startImageExportStatusUpdater(
	ctx context.Context, // Parent context (e.g., consumer context) - NOT the export context
	cancelExport func(), // Cancel function for the export context
	imageExportService imagebuilderapi.ImageExportService,
	orgID uuid.UUID,
	imageExportName string,
	kvStore kvstore.KVStore,
	cfg *config.Config,
	log logrus.FieldLogger,
) (*imageExportStatusUpdater, func()) {
	// Derive updater context from the parent context (consumer context)
	// This is important: do NOT pass exportCtx here - the updater needs to survive
	// export cancellation to write the final Canceled status
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
		cancelExport:       cancelExport,
		log:                log,
		logBuffer:          make([]string, 0, 500),
	}

	updater.wg.Add(1)
	go updater.run(cfg)

	// Start cancellation listener goroutine (uses Redis Streams)
	updater.wg.Add(1)
	go updater.listenForCancellation()

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

	var pendingCondition *domain.ImageExportCondition
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
					u.updateStatus(pendingCondition, &lastSeenUpdateTime, nil)
					// Also persist logs to DB periodically
					u.persistLogsToDB()
					pendingCondition = nil
				}
			}
		case output := <-u.outputChan:
			now := time.Now().UTC()
			lastOutputTime = &now
			u.log.Debugf("Task output: %s", string(output))
			// Write to Redis and update in-memory buffer
			u.writeLogToRedis(output)
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
				u.updateStatus(pendingCondition, &lastSeenUpdateTime, req.ManifestDigest)
				pendingCondition = nil
			}
			// Signal completion if done channel exists (used for synchronous updates)
			if req.done != nil {
				close(req.done)
			}
		}
	}
}

// updateStatus performs the actual database update
// Note: Uses u.ctx (updaterCtx) which is derived from the consumer context, NOT the export context.
// When cancelExport() is called, only exportCtx is canceled - updaterCtx remains valid until
// cleanupStatusUpdater() is called, which happens AFTER processImageExport() returns.
// This ensures we can still write the final status (e.g., Canceled) after the export is canceled.
func (u *imageExportStatusUpdater) updateStatus(condition *domain.ImageExportCondition, lastSeen *time.Time, manifestDigest *string) {
	imageExport, status := u.imageExportService.Get(u.ctx, u.orgID, u.imageExportName)
	if imageExport == nil || !imagebuilderapi.IsStatusOK(status) {
		u.log.WithField("status", status).Warn("Failed to load ImageExport for status update")
		return
	}

	if imageExport.Status == nil {
		imageExport.Status = &domain.ImageExportStatus{}
	}

	if lastSeen != nil {
		imageExport.Status.LastSeen = lastSeen
	}

	if condition != nil {
		if imageExport.Status.Conditions == nil {
			imageExport.Status.Conditions = &[]domain.ImageExportCondition{}
		}

		// Check if current status is "Canceling" - don't overwrite with in-progress states
		// Only allow transitioning to terminal states (Canceled, Failed, Completed)
		skipConditionUpdate := false
		currentCondition := domain.FindImageExportStatusCondition(*imageExport.Status.Conditions, domain.ImageExportConditionTypeReady)
		if currentCondition != nil && currentCondition.Reason == string(domain.ImageExportConditionReasonCanceling) {
			// Only allow terminal states to overwrite Canceling
			if condition.Reason != string(domain.ImageExportConditionReasonCanceled) &&
				condition.Reason != string(domain.ImageExportConditionReasonFailed) &&
				condition.Reason != string(domain.ImageExportConditionReasonCompleted) {
				u.log.WithField("attemptedReason", condition.Reason).Info("Skipping condition update - export is being canceled")
				skipConditionUpdate = true
			}
		}

		if !skipConditionUpdate {
			domain.SetImageExportStatusCondition(imageExport.Status.Conditions, *condition)

			// If export is completed, failed, or canceled, persist logs to DB and signal stream completion
			if condition.Reason == string(domain.ImageExportConditionReasonCompleted) ||
				condition.Reason == string(domain.ImageExportConditionReasonFailed) ||
				condition.Reason == string(domain.ImageExportConditionReasonCanceled) {
				u.persistLogsToDB()
				u.writeStreamCompleteMarker()
				// Signal cancellation completion so Delete API can proceed
				if condition.Reason == string(domain.ImageExportConditionReasonCanceled) {
					u.signalCanceled()
				}
			}
		}
	}

	if manifestDigest != nil {
		imageExport.Status.ManifestDigest = manifestDigest
	}

	_, err := u.imageExportService.UpdateStatus(u.ctx, u.orgID, imageExport)
	if err != nil {
		u.log.WithError(err).Warn("Failed to update ImageExport status")
	}
}

// updateCondition sends a condition update request to the updater goroutine
// For terminal conditions (Completed, Failed, Canceled), this blocks until the update is processed.
// This ensures the final status is written before the caller returns and triggers cleanup.
func (u *imageExportStatusUpdater) updateCondition(condition domain.ImageExportCondition) {
	// For terminal conditions, wait for the update to complete
	isTerminal := condition.Reason == string(domain.ImageExportConditionReasonCompleted) ||
		condition.Reason == string(domain.ImageExportConditionReasonFailed) ||
		condition.Reason == string(domain.ImageExportConditionReasonCanceled)

	req := newImageExportStatusUpdateRequest()
	req.Condition = &condition
	if isTerminal {
		req.done = make(chan struct{})
	}

	select {
	case u.updateChan <- req:
		// If terminal, wait for the update to complete
		if req.done != nil {
			<-req.done
		}
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
func (u *imageExportStatusUpdater) writeLogToRedis(output []byte) {
	if u.kvStore == nil {
		return
	}

	key := u.getLogKey()

	// Write full logs to Redis stream
	_, err := u.kvStore.StreamAdd(u.ctx, key, output)
	if err != nil {
		u.log.WithError(err).Warn("Failed to write log to Redis")
		return
	}

	// Set TTL on the key (1 hour)
	if err := u.kvStore.SetExpire(u.ctx, key, 1*time.Hour); err != nil {
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
func (u *imageExportStatusUpdater) persistLogsToDB() {
	u.logBufferMu.Lock()
	defer u.logBufferMu.Unlock()

	// Get the last 500 lines from buffer
	logs := strings.Join(u.logBuffer, "\n")
	if logs != "" && !strings.HasSuffix(logs, "\n") {
		logs += "\n"
	}

	// Sanitize logs to remove invalid UTF-8 sequences (PostgreSQL requires valid UTF-8)
	logs = strings.ToValidUTF8(logs, "")

	// Persist to DB using the service's UpdateLogs method
	if logs != "" {
		u.log.Debugf("Persisting %d lines of logs to DB", len(u.logBuffer))
		if err := u.imageExportService.UpdateLogs(u.ctx, u.orgID, u.imageExportName, logs); err != nil {
			u.log.WithError(err).Warn("Failed to persist logs to DB")
		}
	}
}

// writeStreamCompleteMarker writes a completion marker to Redis to signal that log streaming is complete
// This allows clients following the log stream to know the stream has ended
func (u *imageExportStatusUpdater) writeStreamCompleteMarker() {
	if u.kvStore == nil {
		return
	}

	key := u.getLogKey()
	_, err := u.kvStore.StreamAdd(u.ctx, key, []byte(domain.LogStreamCompleteMarker))
	if err != nil {
		u.log.WithError(err).Warn("Failed to write stream complete marker to Redis")
	}
}

// GetCanceledStreamKey returns the Redis stream key used for signaling cancellation completion
func GetCanceledStreamKey(orgID uuid.UUID, imageExportName string) string {
	return fmt.Sprintf("imageexport:canceled:%s:%s", orgID.String(), imageExportName)
}

// signalCanceled writes a signal to Redis when the export has been canceled
// This allows the Delete API to wait for cancellation completion before deleting
func (u *imageExportStatusUpdater) signalCanceled() {
	if u.kvStore == nil {
		return
	}

	streamKey := GetCanceledStreamKey(u.orgID, u.imageExportName)
	if _, err := u.kvStore.StreamAdd(u.ctx, streamKey, []byte("canceled")); err != nil {
		u.log.WithError(err).Warn("Failed to write canceled signal to Redis")
		return
	}
	// Set TTL on stream key (5 minutes - enough for delete to read it)
	if err := u.kvStore.SetExpire(u.ctx, streamKey, 5*time.Minute); err != nil {
		u.log.WithError(err).Warn("Failed to set TTL on canceled stream key")
	}
	u.log.Info("Signaled cancellation completion via Redis")
}

// listenForCancellation listens for cancellation messages via Redis Stream
// When a cancellation message is received, it calls the cancelExport function to stop the export
func (u *imageExportStatusUpdater) listenForCancellation() {
	defer u.wg.Done()

	if u.kvStore == nil {
		u.log.Warn("KVStore is nil, cancellation listener not started")
		return
	}

	streamKey := fmt.Sprintf("imageexport:cancel:%s:%s", u.orgID.String(), u.imageExportName)
	lastID := "0" // Read from beginning

	for {
		select {
		case <-u.ctx.Done():
			return
		default:
			// Blocking read with 2 second timeout (then loop to check ctx.Done)
			entries, err := u.kvStore.StreamRead(u.ctx, streamKey, lastID, 2*time.Second, 1)
			if err != nil {
				// Context canceled is expected during shutdown
				if u.ctx.Err() != nil {
					return
				}
				u.log.WithError(err).Debug("Error reading cancellation stream")
				continue
			}
			if len(entries) > 0 {
				// Update lastID to avoid re-reading the same message
				lastID = entries[0].ID

				// Verify the ImageExport status is actually "Canceling" before acting
				// This prevents stale cancellation messages from previous attempts
				// from canceling a new export
				if !u.isStatusCanceling() {
					u.log.Debug("Ignoring stale cancellation message - status is not Canceling")
					continue
				}

				u.log.Info("Cancellation received via Redis Stream, canceling export")

				// Delete the stream key to consume the signal exactly once
				// This prevents the same signal from being processed again on retry
				if err := u.kvStore.Delete(u.ctx, streamKey); err != nil {
					u.log.WithError(err).Warn("Failed to delete cancellation stream after consuming")
				}

				if u.cancelExport != nil {
					u.cancelExport()
				}
				return // Exit after cancellation
			}
		}
	}
}

// isStatusCanceling checks if the current ImageExport status is "Canceling"
func (u *imageExportStatusUpdater) isStatusCanceling() bool {
	imageExport, status := u.imageExportService.Get(u.ctx, u.orgID, u.imageExportName)
	if imageExport == nil || !imagebuilderapi.IsStatusOK(status) {
		return false
	}

	if imageExport.Status == nil || imageExport.Status.Conditions == nil {
		return false
	}

	readyCondition := domain.FindImageExportStatusCondition(*imageExport.Status.Conditions, domain.ImageExportConditionTypeReady)
	if readyCondition == nil {
		return false
	}

	return readyCondition.Reason == string(domain.ImageExportConditionReasonCanceling)
}
