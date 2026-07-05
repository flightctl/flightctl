package console

import "time"

const (
	// cleanupDuration is how long inactive sessions are retained before
	// being removed from the inactive list.
	cleanupDuration = 5 * time.Minute
)
