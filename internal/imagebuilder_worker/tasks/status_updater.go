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

// statusUpdateRequest represents a request to update the ImageBuild status
type statusUpdateRequest struct {
	Condition      *api.ImageBuildCondition
	LastSeen       *time.Time
	ImageReference *string
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
	wg                sync.WaitGroup
	log               logrus.FieldLogger
	// logBuffer keeps the last 500 lines of logs in memory
	logBuffer   []string
	logBufferMu sync.Mutex
}

// StartStatusUpdater starts a goroutine that is the single writer for ImageBuild status updates.
// It receives condition updates via a channel and periodically updates LastSeen.
// Returns the updater and a cleanup function.
// Exported for testing purposes.
func StartStatusUpdater(
	ctx context.Context,
	imageBuildService imagebuilderapi.ImageBuildService,
	orgID uuid.UUID,
	imageBuildName string,
	kvStore kvstore.KVStore,
	cfg *config.Config,
	log logrus.FieldLogger,
) (*statusUpdater, func()) {
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
		log:               log,
		logBuffer:         make([]string, 0, 500),
	}

	updater.wg.Add(1)
	go updater.run(cfg)

	cleanup := func() {
		updaterCancel()
		close(updater.updateChan)
		close(updater.outputChan)
		updater.wg.Wait()
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
	var pendingCondition *api.ImageBuildCondition
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
					u.updateStatus(u.ctx, pendingCondition, &lastSeenUpdateTime, pendingImageReference)
					// Also persist logs to DB periodically
					u.persistLogsToDB(u.ctx)
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
				u.updateStatus(u.ctx, pendingCondition, &lastSeenUpdateTime, pendingImageReference)
				pendingCondition = nil      // Clear after update
				pendingImageReference = nil // Clear after update
			}
		}
	}
}

// updateStatus performs the actual database update, merging conditions, LastSeen, and ImageReference
func (u *statusUpdater) updateStatus(ctx context.Context, condition *api.ImageBuildCondition, lastSeen *time.Time, imageReference *string) {
	// Load current status from database
	imageBuild, status := u.imageBuildService.Get(ctx, u.orgID, u.imageBuildName, false)
	if imageBuild == nil || !imagebuilderapi.IsStatusOK(status) {
		u.log.WithField("status", status).Warn("Failed to load ImageBuild for status update")
		return
	}

	// Initialize status if needed
	if imageBuild.Status == nil {
		imageBuild.Status = &api.ImageBuildStatus{}
	}

	// Update LastSeen
	if lastSeen != nil {
		imageBuild.Status.LastSeen = lastSeen
	}

	// Update condition if provided
	if condition != nil {
		if imageBuild.Status.Conditions == nil {
			imageBuild.Status.Conditions = &[]api.ImageBuildCondition{}
		}

		// Use helper function to set condition, keeping ImageBuildCondition type
		api.SetImageBuildStatusCondition(imageBuild.Status.Conditions, *condition)

		// If build is completed or failed, persist logs to DB
		if condition.Reason == string(api.ImageBuildConditionReasonCompleted) ||
			condition.Reason == string(api.ImageBuildConditionReasonFailed) {
			u.persistLogsToDB(ctx)
		}
	}

	// Update ImageReference if provided
	if imageReference != nil {
		imageBuild.Status.ImageReference = imageReference
	}

	// Write updated status atomically
	_, err := u.imageBuildService.UpdateStatus(ctx, u.orgID, imageBuild)
	if err != nil {
		u.log.WithError(err).Warn("Failed to update ImageBuild status")
	}
}

// UpdateCondition sends a condition update request to the updater goroutine
// Exported for testing purposes.
func (u *statusUpdater) UpdateCondition(condition api.ImageBuildCondition) {
	select {
	case u.updateChan <- statusUpdateRequest{Condition: &condition}:
	case <-u.ctx.Done():
		// Context cancelled, ignore update
	}
}

// UpdateImageReference sends an image reference update request to the updater goroutine
// Exported for testing purposes.
func (u *statusUpdater) UpdateImageReference(imageReference string) {
	select {
	case u.updateChan <- statusUpdateRequest{ImageReference: &imageReference}:
	case <-u.ctx.Done():
		// Context cancelled, ignore update
	}
}

// ReportOutput sends task output to the central output handler
// This marks that progress has been made and LastSeen should be updated
// Exported for testing purposes.
func (u *statusUpdater) ReportOutput(output []byte) {
	select {
	case u.outputChan <- output:
	case <-u.ctx.Done():
		// Context cancelled, ignore output
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
func (u *statusUpdater) persistLogsToDB(ctx context.Context) {
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
		if err := u.imageBuildService.UpdateLogs(ctx, u.orgID, u.imageBuildName, logs); err != nil {
			u.log.WithError(err).Warn("Failed to persist logs to DB")
		}
	}
}
