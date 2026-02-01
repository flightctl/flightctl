package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const (
	// Redis key pattern for imagebuild logs
	imageBuildLogsKeyPattern = "imagebuild:logs:%s:%s"
	// Redis key pattern for imageexport logs
	imageExportLogsKeyPattern = "imageexport:logs:%s:%s"
)

// LogStreamReader provides an interface for reading logs from Redis
type LogStreamReader interface {
	// ReadAll reads all available logs from Redis and returns them as a string
	ReadAll(ctx context.Context) (string, error)
	// Stream reads logs from Redis and writes them to the provided writer
	// It blocks until the context is cancelled or an error occurs
	Stream(ctx context.Context, w io.Writer) error
}

// redisLogStreamReader implements LogStreamReader for Redis stream operations
type redisLogStreamReader struct {
	kvStore kvstore.KVStore
	key     string
	log     logrus.FieldLogger
	lastID  string // Track the last message ID read for streaming
}

// newRedisLogStreamReader creates a new Redis log stream reader for ImageBuild
func newRedisLogStreamReader(kvStore kvstore.KVStore, orgID uuid.UUID, name string, log logrus.FieldLogger) *redisLogStreamReader {
	key := fmt.Sprintf(imageBuildLogsKeyPattern, orgID.String(), name)
	return &redisLogStreamReader{
		kvStore: kvStore,
		key:     key,
		log:     log,
		lastID:  "0", // Start from beginning
	}
}

// newImageExportRedisLogStreamReader creates a new Redis log stream reader for ImageExport
func newImageExportRedisLogStreamReader(kvStore kvstore.KVStore, orgID uuid.UUID, name string, log logrus.FieldLogger) *redisLogStreamReader {
	key := fmt.Sprintf(imageExportLogsKeyPattern, orgID.String(), name)
	return &redisLogStreamReader{
		kvStore: kvStore,
		key:     key,
		log:     log,
		lastID:  "0", // Start from beginning
	}
}

// ReadAll reads all available logs from Redis stream
// Returns the logs and a boolean indicating if the stream is complete
func (r *redisLogStreamReader) ReadAll(ctx context.Context) (string, error) {
	// Get all items from the stream ("-" to "+" means all items)
	entries, err := r.kvStore.StreamRange(ctx, r.key, "-", "+")
	if err != nil {
		return "", fmt.Errorf("failed to read logs from Redis: %w", err)
	}

	var logLines []string
	for _, entry := range entries {
		logLine := string(entry.Value)
		// Skip the completion marker - it's not actual log content
		if logLine == domain.LogStreamCompleteMarker {
			r.lastID = entry.ID
			continue
		}
		// Ensure each log line ends with a newline
		if !strings.HasSuffix(logLine, "\n") {
			logLine += "\n"
		}
		logLines = append(logLines, logLine)
		// Update lastID for potential streaming continuation
		r.lastID = entry.ID
	}
	return strings.Join(logLines, ""), nil
}

// Stream reads logs from Redis stream and streams them to the writer
// For active builds, it uses XREAD with blocking to wait for new log entries
// Returns nil when the stream is complete (LogStreamCompleteMarker received)
func (r *redisLogStreamReader) Stream(ctx context.Context, w io.Writer) error {
	// First, send all existing logs
	allLogs, err := r.ReadAll(ctx)
	if err != nil {
		return err
	}
	if len(allLogs) > 0 {
		if _, err := w.Write([]byte(allLogs)); err != nil {
			return fmt.Errorf("failed to write existing logs: %w", err)
		}
		// Flush if possible
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}

	// Then, stream new logs using XREAD with blocking
	// Start from lastID to continue where ReadAll() left off
	// Note: We intentionally keep "0" instead of switching to "$" to avoid missing
	// entries written between ReadAll() returning and StreamRead() blocking
	startID := r.lastID

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Use XREAD with blocking to wait for new entries
			entries, err := r.kvStore.StreamRead(ctx, r.key, startID, 1*time.Second, 0)
			if err != nil {
				return fmt.Errorf("failed to read from Redis: %w", err)
			}

			for _, entry := range entries {
				logLine := string(entry.Value)
				// Check for completion marker - stream is complete (orderly close)
				// Forward the marker to the client so it knows the stream ended orderly
				if logLine == domain.LogStreamCompleteMarker {
					r.log.Debug("Stream complete marker received, forwarding to client and closing stream")
					// Write the marker to the client so it can differentiate orderly vs abrupt close
					if _, err := w.Write([]byte(domain.LogStreamCompleteMarker)); err != nil {
						return fmt.Errorf("failed to write completion marker: %w", err)
					}
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
					return nil
				}
				if !strings.HasSuffix(logLine, "\n") {
					logLine += "\n"
				}
				if _, err := w.Write([]byte(logLine)); err != nil {
					return fmt.Errorf("failed to write log line: %w", err)
				}
				// Update lastID for next read
				startID = entry.ID
				r.lastID = entry.ID
			}

			// Flush if we wrote any data
			if len(entries) > 0 {
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
		}
	}
}
