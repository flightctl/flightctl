package queues

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalculateBackoff(t *testing.T) {
	tests := []struct {
		name        string
		retryCount  int
		config      RetryConfig
		expectedMin time.Duration
		expectedMax time.Duration
	}{
		{
			name:       "When retryCount is 0 it should return BaseDelay",
			retryCount: 0,
			config: RetryConfig{
				BaseDelay:    1 * time.Second,
				MaxDelay:     1 * time.Hour,
				JitterFactor: 0,
			},
			expectedMin: 1 * time.Second,
			expectedMax: 1 * time.Second,
		},
		{
			name:       "When retryCount is 1 it should double the BaseDelay",
			retryCount: 1,
			config: RetryConfig{
				BaseDelay:    1 * time.Second,
				MaxDelay:     1 * time.Hour,
				JitterFactor: 0,
			},
			expectedMin: 2 * time.Second,
			expectedMax: 2 * time.Second,
		},
		{
			name:       "When retryCount is 3 it should apply 2^3 multiplier",
			retryCount: 3,
			config: RetryConfig{
				BaseDelay:    1 * time.Second,
				MaxDelay:     1 * time.Hour,
				JitterFactor: 0,
			},
			expectedMin: 8 * time.Second,
			expectedMax: 8 * time.Second,
		},
		{
			name:       "When delay exceeds MaxDelay it should be capped",
			retryCount: 10,
			config: RetryConfig{
				BaseDelay:    1 * time.Second,
				MaxDelay:     5 * time.Second,
				JitterFactor: 0,
			},
			expectedMin: 5 * time.Second,
			expectedMax: 5 * time.Second,
		},
		{
			name:       "When JitterFactor is set it should return delay within jitter bounds",
			retryCount: 0,
			config: RetryConfig{
				BaseDelay:    10 * time.Second,
				MaxDelay:     1 * time.Hour,
				JitterFactor: 0.1, // ±10% of 10s = ±1s → range [9s, 11s]
			},
			expectedMin: 9 * time.Second,
			expectedMax: 11 * time.Second,
		},
		{
			name:       "When capped at MaxDelay with jitter it should not exceed MaxDelay plus jitter",
			retryCount: 10,
			config: RetryConfig{
				BaseDelay:    1 * time.Second,
				MaxDelay:     5 * time.Second,
				JitterFactor: 0.1, // ±10% of 5s = ±0.5s → range [4.5s, 5.5s]
			},
			expectedMin: 4500 * time.Millisecond,
			expectedMax: 5500 * time.Millisecond,
		},
		{
			name:       "When delay is negative after jitter it should clamp to zero",
			retryCount: 0,
			config: RetryConfig{
				BaseDelay:    1 * time.Nanosecond,
				MaxDelay:     1 * time.Hour,
				JitterFactor: 1.0,
			},
			expectedMin: 0,
			expectedMax: 2 * time.Nanosecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Run multiple times to account for jitter randomness
			for i := 0; i < 100; i++ {
				got := calculateBackoff(tt.retryCount, tt.config)
				assert.GreaterOrEqual(t, got, tt.expectedMin, "iteration %d: delay below minimum", i)
				assert.LessOrEqual(t, got, tt.expectedMax, "iteration %d: delay above maximum", i)
			}
		})
	}
}

func TestFormatAndParseFailedMember(t *testing.T) {
	q := &redisQueue{
		name:      "test-stream",
		processID: "test-process",
	}

	tests := []struct {
		name       string
		entryID    string
		body       []byte
		retryCount int
	}{
		{
			name:       "When body is plain text it should round-trip correctly",
			entryID:    "1234567890-0",
			body:       []byte(`{"device":"foo","status":"ok"}`),
			retryCount: 0,
		},
		{
			name:       "When body contains pipe characters it should round-trip correctly",
			entryID:    "9999999999-1",
			body:       []byte("body|with|pipes"),
			retryCount: 2,
		},
		{
			name:       "When body is empty it should round-trip correctly",
			entryID:    "1111111111-0",
			body:       []byte{},
			retryCount: 5,
		},
		{
			name:       "When body is binary it should round-trip correctly",
			entryID:    "2222222222-0",
			body:       []byte{0x00, 0xFF, 0x1A, 0xFE},
			retryCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			formatted := q.formatFailedMember(tt.entryID, tt.body, tt.retryCount)

			streamName, entryID, body, processID, retryCount, err := q.parseFailedMember(formatted)

			require.NoError(t, err)
			assert.Equal(t, q.name, streamName)
			assert.Equal(t, tt.entryID, entryID)
			assert.Equal(t, tt.body, body)
			assert.Equal(t, q.processID, processID)
			assert.Equal(t, tt.retryCount, retryCount)
		})
	}
}

func TestParseFailedMemberErrors(t *testing.T) {
	q := &redisQueue{name: "test-stream", processID: "test-process"}

	tests := []struct {
		name   string
		input  string
		errMsg string
	}{
		{
			name:   "When input has too few fields it should return an error",
			input:  "stream|entryID|body",
			errMsg: "invalid failed member format",
		},
		{
			name:   "When retryCount is not a number it should return an error",
			input:  "stream|entryID|aGVsbG8=|process|notanumber",
			errMsg: "invalid retry count",
		},
		{
			name:   "When body is not valid base64 it should return an error",
			input:  "stream|entryID|!!!invalid-base64!!!|process|0",
			errMsg: "failed to decode base64 body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, _, _, err := q.parseFailedMember(tt.input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

func TestExtractRetryCountFromValue(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected int
	}{
		{
			name:     "When value is an int it should return it directly",
			input:    int(3),
			expected: 3,
		},
		{
			name:     "When value is an int64 it should convert correctly",
			input:    int64(7),
			expected: 7,
		},
		{
			name:     "When value is a numeric string it should parse it",
			input:    "5",
			expected: 5,
		},
		{
			name:     "When value is a non-numeric string it should return 0",
			input:    "notanumber",
			expected: 0,
		},
		{
			name:     "When value is nil it should return 0",
			input:    nil,
			expected: 0,
		},
		{
			name:     "When value is a float64 it should return 0",
			input:    float64(3.5),
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRetryCountFromValue(tt.input)
			assert.Equal(t, tt.expected, got)
		})
	}
}
