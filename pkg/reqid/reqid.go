package reqid

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
)

var prefixMutex sync.Mutex
var prefix string
var reqid uint64

func init() {
	hostname, err := os.Hostname()
	if hostname == "" || err != nil {
		hostname = "localhost"
	}

	prefixMutex.Lock()
	prefix = hostname
	prefixMutex.Unlock()
}

func OverridePrefix(p string) {
	prefixMutex.Lock()
	prefix = p
	prefixMutex.Unlock()
}

// NextRequestID generates the next request ID in the sequence.
func NextRequestID() string {
	prefixMutex.Lock()
	defer prefixMutex.Unlock()
	currentID := atomic.LoadUint64(&reqid)
	return fmt.Sprintf("%s-%09d", prefix, currentID)
}

func GetReqID() string {
	prefixMutex.Lock()
	defer prefixMutex.Unlock()
	return fmt.Sprintf("%s-%09d", prefix, reqid)
}
