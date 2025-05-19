package reqid

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// This package generates request IDs using code lifted from chi middleware.
// We copy the code here because we need request IDs in different contexts,
// not just HTTP requests.

var prefixMutex sync.Mutex
var prefix string
var reqid uint64

func init() {
	hostname, err := os.Hostname()
	if hostname == "" || err != nil {
		hostname = "localhost"
	}
	setPrefix(hostname)
}

func OverridePrefix(p string) {
	setPrefix(p)
}

func setPrefix(p string) {
	var buf [12]byte
	var b64 string
	for len(b64) < 10 {
		_, _ = rand.Read(buf[:])
		b64 = base64.StdEncoding.EncodeToString(buf[:])
		b64 = strings.NewReplacer("+", "", "/", "").Replace(b64)
	}

	prefixMutex.Lock()
	prefix = fmt.Sprintf("%s/%s", p, b64[0:10])
	prefixMutex.Unlock()
}

func NextRequestID() string {
	myid := atomic.AddUint64(&reqid, 1)
	return fmt.Sprintf("%s-%06d", prefix, myid)
}
