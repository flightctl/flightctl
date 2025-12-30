package certmanager

import (
	"strings"
	"testing"
)

func TestProviderKeyPrefixInvariant(t *testing.T) {
	prefix := providerKeyPrefix("bundle")
	key := providerKey("bundle", "config")

	if !strings.HasPrefix(key, prefix) {
		t.Fatalf("providerKey must share prefix with providerKeyPrefix")
	}
}
