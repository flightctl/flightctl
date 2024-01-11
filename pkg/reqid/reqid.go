package reqid

import (
	"fmt"
	"os"
	"sync/atomic"
)

var prefix string
var reqid uint64

func init() {
	hostname, err := os.Hostname()
	if hostname == "" || err != nil {
		hostname = "localhost"
	}

	prefix = hostname
}

// NextRequestID generates the next request ID in the sequence.
func NextRequestID() string {
	return fmt.Sprintf("%s-%09d", prefix, atomic.AddUint64(&reqid, 1))
}

func GetReqID() string {
	return fmt.Sprintf("%s-%09d", prefix, reqid)
}
