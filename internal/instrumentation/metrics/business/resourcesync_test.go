package business

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResourceSyncCollector(t *testing.T) {
	// Test that the collector name is correct
	assert.Equal(t, "resourcesync", (&ResourceSyncCollector{}).MetricsName())
}
