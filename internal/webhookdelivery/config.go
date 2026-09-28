package webhookdelivery

import "time"

const (
	DefaultMaxAttempts  = 5
	DefaultDeadline     = 10 * time.Minute
	DefaultMaxBackoff   = 2 * time.Minute
	DefaultBackoffDelay = 2 * time.Second
	DefaultTimeout      = 30 * time.Second
)

// Config holds destination and retry settings for a webhook delivery.
// Zero / non-positive duration and attempt fields fall back to defaults.
type Config struct {
	URL          string
	Timeout      time.Duration
	MaxAttempts  int
	Deadline     time.Duration
	MaxBackoff   time.Duration
	BackoffDelay time.Duration
}

// WithDefaults returns a copy with zero / non-positive fields filled from package defaults.
func (c Config) WithDefaults() Config {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = DefaultMaxAttempts
	}
	if c.Deadline <= 0 {
		c.Deadline = DefaultDeadline
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = DefaultMaxBackoff
	}
	if c.BackoffDelay <= 0 {
		c.BackoffDelay = DefaultBackoffDelay
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	return c
}
