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

// statusUpdateRequest represents a request to update the ImageBuild status
type statusUpdateRequest struct {
	Condition      *domain.ImageBuildCondition
	LastSeen       *time.Time
	ImageReference *string
	// done is closed when the update has been processed (used for terminal conditions)
	done chan struct{}
}

// statusUpdater manages all status updates for an ImageBuild, ensuring atomic updates
// and preventing race conditions between LastSeen and condition updates.
// It also tracks task outputs and only updates LastSeen when new data is received.
type statusUpdater struct {
	imageBuildService imagebuilderapi.ImageBuildService
	orgID             uuid.UUID
	imageBuildName    string
	kvStore           kvstore.KVStore
	updateChan        chan statusUpdateRequest
	outputChan        chan []byte // Central channel for all task outputs
	ctx               context.Context
	cancel            context.CancelFunc
	cancelBuild       func() // Cancel function for the build context (to stop build on cancellation)
	wg                sync.WaitGroup
	log               logrus.FieldLogger
	// logBuffer keeps the last 500 lines of logs in memory
	logBuffer   []string
	logBufferMu sync.Mutex
}

// StartStatusUpdater starts a goroutine that is the single writer for ImageBuild status updates.
// It receives condition updates via a channel and periodically updates LastSeen.
// It also listens for cancellation signals via Redis Stream and calls cancelBuild when received.
// Returns the updater and a cleanup function.
// Exported for testing purposes.
func StartStatusUpdater(
	ctx context.Context, // Parent context (e.g., consumer context) - NOT the build context
	cancelBuild func(), // Cancel function for the build context
	imageBuildService imagebuilderapi.ImageBuildService,
	orgID uuid.UUID,
	imageBuildName string,
	kvStore kvstore.KVStore,
	cfg *config.Config,
	log logrus.FieldLogger,
) (*statusUpdater, func()) {
	// Derive updater context from the parent context (consumer context)
	// This is important: do NOT pass buildCtx here - the updater needs to survive
	// build cancellation to write the final Canceled status
	updaterCtx, updaterCancel := context.WithCancel(ctx)

	updater := &statusUpdater{
		imageBuildService: imageBuildService,
		orgID:             orgID,
		imageBuildName:    imageBuildName,
		kvStore:           kvStore,
		updateChan:        make(chan statusUpdateRequest), // Unbuffered channel - blocks until processed
		outputChan:        make(chan []byte, 100),         // Buffered channel for task outputs
		ctx:               updaterCtx,
		cancel:            updaterCancel,
		cancelBuild:       cancelBuild,
		log:               log,
		logBuffer:         make([]string, 0, 500),
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
func (u *statusUpdater) run(cfg *config.Config) {
	defer u.wg.Done()

	// Use LastSeenUpdateInterval from config (defaults are applied during config loading)
	if cfg == nil || cfg.ImageBuilderWorker == nil {
		u.log.Error("Config or ImageBuilderWorker config is nil, cannot update status")
		return
	}
	updateInterval := time.Duration(cfg.ImageBuilderWorker.LastSeenUpdateInterval)
	ticker := time.NewTicker(updateInterval)
	defer ticker.Stop()

	// Track pending updates
	var pendingCondition *domain.ImageBuildCondition
	var pendingImageReference *string
	lastSeenUpdateTime := time.Now().UTC()

	// Track the last time output was received - updated when new output arrives
	var lastOutputTime *time.Time
	// Track the last LastSeen value we set in the database
	var lastSetLastSeen *time.Time

	for {
		select {
		case <-u.ctx.Done():
			return
		case <-ticker.C:
			// Periodic LastSeen update - only if we have new output time and haven't set it yet
			if lastOutputTime != nil {
				// Only update if this is a different time than what we last set
				if lastSetLastSeen == nil || !lastOutputTime.Equal(*lastSetLastSeen) {
					lastSeenUpdateTime = *lastOutputTime
					// Store a copy of the time we're setting
					lastSetLastSeenCopy := *lastOutputTime
					lastSetLastSeen = &lastSetLastSeenCopy
					u.updateStatus(pendingCondition, &lastSeenUpdateTime, pendingImageReference)
					// Also persist logs to DB periodically
					u.persistLogsToDB()
					pendingCondition = nil      // Clear after update
					pendingImageReference = nil // Clear after update
				}
			}
		case output := <-u.outputChan:
			// Task output received - update local variable with current time
			now := time.Now().UTC()
			lastOutputTime = &now
			// Log output for debugging (can be removed or made conditional)
			u.log.Debugf("Task output: %s", string(output))
			// Write to Redis and update in-memory buffer
			u.writeLogToRedis(u.ctx, output)
		case req := <-u.updateChan:
			// Status update requested
			if req.Condition != nil {
				pendingCondition = req.Condition
			}
			if req.LastSeen != nil {
				lastSeenUpdateTime = *req.LastSeen
			}
			if req.ImageReference != nil {
				pendingImageReference = req.ImageReference
			}
			// Update immediately when condition or image reference changes
			if req.Condition != nil || req.ImageReference != nil {
				u.updateStatus(pendingCondition, &lastSeenUpdateTime, pendingImageReference)
				pendingCondition = nil      // Clear after update
				pendingImageReference = nil // Clear after update
			}
			// Signal completion if done channel exists (used for synchronous updates)
			if req.done != nil {
				close(req.done)
			}
		}
	}
}

// updateStatus performs the actual database update, merging conditions, LastSeen, and ImageReference
// Note: Uses u.ctx (updaterCtx) which is derived from the consumer context, NOT the build context.
// When cancelBuild() is called, only buildCtx is canceled - updaterCtx remains valid until
// cleanupStatusUpdater() is called, which happens AFTER processImageBuild() returns.
// This ensures we can still write the final status (e.g., Canceled) after the build is canceled.
func (u *statusUpdater) updateStatus(condition *domain.ImageBuildCondition, lastSeen *time.Time, imageReference *string) {
	// Load current status from database
	imageBuild, status := u.imageBuildService.Get(u.ctx, u.orgID, u.imageBuildName, false)
	if imageBuild == nil || !imagebuilderapi.IsStatusOK(status) {
		u.log.WithField("status", status).Warn("Failed to load ImageBuild for status update")
		return
	}

	// Initialize status if needed
	if imageBuild.Status == nil {
		imageBuild.Status = &domain.ImageBuildStatus{}
	}

	// Update LastSeen
	if lastSeen != nil {
		imageBuild.Status.LastSeen = lastSeen
	}

	// Update condition if provided
	if condition != nil {
		if imageBuild.Status.Conditions == nil {
			imageBuild.Status.Conditions = &[]domain.ImageBuildCondition{}
		}

		// Check if current status is "Canceling" - don't overwrite with in-progress states
		// Only allow transitioning to terminal states (Canceled, Failed, Completed)
		skipConditionUpdate := false
		currentCondition := domain.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, domain.ImageBuildConditionTypeReady)
		if currentCondition != nil && currentCondition.Reason == string(domain.ImageBuildConditionReasonCanceling) {
			// Only allow terminal states to overwrite Canceling
			if condition.Reason != string(domain.ImageBuildConditionReasonCanceled) &&
				condition.Reason != string(domain.ImageBuildConditionReasonFailed) &&
				condition.Reason != string(domain.ImageBuildConditionReasonCompleted) {
				u.log.WithField("attemptedReason", condition.Reason).Info("Skipping condition update - build is being canceled")
				skipConditionUpdate = true
			}
		}

		if !skipConditionUpdate {
			// Use helper function to set condition, keeping ImageBuildCondition type
			domain.SetImageBuildStatusCondition(imageBuild.Status.Conditions, *condition)

			// If build is completed, failed, or canceled, persist logs to DB and signal stream completion
			if condition.Reason == string(domain.ImageBuildConditionReasonCompleted) ||
				condition.Reason == string(domain.ImageBuildConditionReasonFailed) ||
				condition.Reason == string(domain.ImageBuildConditionReasonCanceled) {
				u.persistLogsToDB()
				u.writeStreamCompleteMarker()
				// Signal cancellation completion to the API (for cancel-then-delete flow)
				if condition.Reason == string(domain.ImageBuildConditionReasonCanceled) {
					u.signalCanceled()
				}
			}
		}
	}

	// Update ImageReference if provided
	if imageReference != nil {
		imageBuild.Status.ImageReference = imageReference
	}

	// Write updated status atomically
	_, err := u.imageBuildService.UpdateStatus(u.ctx, u.orgID, imageBuild)
	if err != nil {
		u.log.WithError(err).Warn("Failed to update ImageBuild status")
	}
}

// UpdateCondition sends a condition update request to the updater goroutine
// For terminal conditions (Completed, Failed, Canceled), this blocks until the update is processed.
// This ensures the final status is written before the caller returns and triggers cleanup.
// Exported for testing purposes.
func (u *statusUpdater) UpdateCondition(condition domain.ImageBuildCondition) {
	// For terminal conditions, wait for the update to complete
	isTerminal := condition.Reason == string(domain.ImageBuildConditionReasonCompleted) ||
		condition.Reason == string(domain.ImageBuildConditionReasonFailed) ||
		condition.Reason == string(domain.ImageBuildConditionReasonCanceled)

	var done chan struct{}
	if isTerminal {
		done = make(chan struct{})
	}

	select {
	case u.updateChan <- statusUpdateRequest{Condition: &condition, done: done}:
		// If terminal, wait for the update to complete
		if done != nil {
			<-done
		}
	case <-u.ctx.Done():
		// Context canceled, ignore update
	}
}

// UpdateImageReference sends an image reference update request to the updater goroutine
// Exported for testing purposes.
func (u *statusUpdater) UpdateImageReference(imageReference string) {
	select {
	case u.updateChan <- statusUpdateRequest{ImageReference: &imageReference}:
	case <-u.ctx.Done():
		// Context canceled, ignore update
	}
}

// ReportOutput sends task output to the central output handler
// This marks that progress has been made and LastSeen should be updated
// Exported for testing purposes.
func (u *statusUpdater) ReportOutput(output []byte) {
	select {
	case u.outputChan <- output:
	case <-u.ctx.Done():
		// Context canceled, ignore output
	}
}

// getLogKey returns the Redis key for this ImageBuild's logs
func (u *statusUpdater) getLogKey() string {
	return fmt.Sprintf("imagebuild:logs:%s:%s", u.orgID.String(), u.imageBuildName)
}

// writeLogToRedis writes log output to Redis and updates the in-memory buffer
// The in-memory buffer keeps only the last 500 lines to save memory
func (u *statusUpdater) writeLogToRedis(ctx context.Context, output []byte) {
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
func (u *statusUpdater) persistLogsToDB() {
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
		if err := u.imageBuildService.UpdateLogs(u.ctx, u.orgID, u.imageBuildName, logs); err != nil {
			u.log.WithError(err).Warn("Failed to persist logs to DB")
		}
	}
}

// writeStreamCompleteMarker writes a completion marker to Redis to signal that log streaming is complete
// This allows clients following the log stream to know the stream has ended
func (u *statusUpdater) writeStreamCompleteMarker() {
	if u.kvStore == nil {
		return
	}

	key := u.getLogKey()
	_, err := u.kvStore.StreamAdd(u.ctx, key, []byte(domain.LogStreamCompleteMarker))
	if err != nil {
		u.log.WithError(err).Warn("Failed to write stream complete marker to Redis")
	}
}

// listenForCancellation listens for cancellation messages via Redis Stream
// When a cancellation message is received, it calls the cancelBuild function to stop the build
func (u *statusUpdater) listenForCancellation() {
	defer u.wg.Done()

	if u.kvStore == nil {
		u.log.Warn("KVStore is nil, cancellation listener not started")
		return
	}

	streamKey := getImageBuildCancelStreamKey(u.orgID, u.imageBuildName)
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

				// Verify the ImageBuild status is actually "Canceling" before acting
				// This prevents stale cancellation messages from previous attempts
				// from canceling a new build
				if !u.isStatusCanceling() {
					u.log.Debug("Ignoring stale cancellation message - status is not Canceling")
					continue
				}

				u.log.Info("Cancellation received via Redis Stream, canceling build")

				// Delete the stream key to consume the signal exactly once
				// This prevents the same signal from being processed again on retry
				if err := u.kvStore.Delete(u.ctx, streamKey); err != nil {
					u.log.WithError(err).Warn("Failed to delete cancellation stream after consuming")
				}

				if u.cancelBuild != nil {
					u.cancelBuild()
				}
				return // Exit after cancellation
			}
		}
	}
}

// isStatusCanceling checks if the current ImageBuild status is "Canceling"
func (u *statusUpdater) isStatusCanceling() bool {
	imageBuild, status := u.imageBuildService.Get(u.ctx, u.orgID, u.imageBuildName, false)
	if imageBuild == nil || !imagebuilderapi.IsStatusOK(status) {
		return false
	}

	if imageBuild.Status == nil || imageBuild.Status.Conditions == nil {
		return false
	}

	readyCondition := domain.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, domain.ImageBuildConditionTypeReady)
	if readyCondition == nil {
		return false
	}

	return readyCondition.Reason == string(domain.ImageBuildConditionReasonCanceling)
}

// getImageBuildCancelStreamKey returns the Redis stream key for cancellation requests
func getImageBuildCancelStreamKey(orgID uuid.UUID, imageBuildName string) string {
	return fmt.Sprintf("imagebuild:cancel:%s:%s", orgID.String(), imageBuildName)
}

// GetImageBuildCanceledStreamKey returns the Redis stream key used for signaling cancellation completion
// This is exported so the API service can use the same key format
func GetImageBuildCanceledStreamKey(orgID uuid.UUID, imageBuildName string) string {
	return fmt.Sprintf("imagebuild:canceled:%s:%s", orgID.String(), imageBuildName)
}

// signalCanceled writes a signal to Redis when the build has been canceled
// This allows the API to know when cancellation is complete for the delete flow
func (u *statusUpdater) signalCanceled() {
	if u.kvStore == nil {
		return
	}

	streamKey := GetImageBuildCanceledStreamKey(u.orgID, u.imageBuildName)
	if _, err := u.kvStore.StreamAdd(u.ctx, streamKey, []byte("canceled")); err != nil {
		u.log.WithError(err).Warn("Failed to write cancellation signal to Redis")
		return
	}

	// Set a TTL so the key is cleaned up even if the API doesn't consume it
	if err := u.kvStore.SetExpire(u.ctx, streamKey, 5*time.Minute); err != nil {
		u.log.WithError(err).Warn("Failed to set TTL on cancellation signal key")
	}

	u.log.Info("Cancellation completion signal sent to Redis")
}
