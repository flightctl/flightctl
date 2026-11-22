package executer

import (
	"bytes"
	"errors"
	"time"
)

var (
	// ErrPhaseUnsupported means the required confinement cannot be installed.
	ErrPhaseUnsupported = errors.New("firmware phase execution profile unavailable")
	// ErrPhaseOutputLimit means a helper exceeded an independent stream budget.
	ErrPhaseOutputLimit = errors.New("firmware phase output exceeds its limit")
	// ErrPhaseCleanup means the runner cannot prove that the execution scope is empty.
	ErrPhaseCleanup = errors.New("firmware phase cleanup could not be proven")
)

// PhaseExecLimits bounds a helper invocation and its teardown.
type PhaseExecLimits struct {
	ResultBytes  int64
	StderrBytes  int64
	ScratchBytes int64
	MemoryBytes  int64
	Processes    int64
	Invocation   time.Duration
	Cleanup      time.Duration
}

// PhaseExecSpec describes a caller-owned, verified execution root kept stable
// until cleanup. Args excludes argv[0]. All paths inside the root are agent-owned
// inputs except the optional ephemeral /work mount. SourceSocket permits only
// Unix sockets in an otherwise isolated network namespace. The caller must mount
// only the explicitly admitted source socket in the root.
type PhaseExecSpec struct {
	RootDir      string
	Entrypoint   string
	Args         []string
	WritableWork bool
	SourceSocket bool
	Limits       PhaseExecLimits
}

// PhaseExecOutput contains independently bounded result and diagnostic bytes.
type PhaseExecOutput struct {
	Result   []byte
	Stderr   []byte
	ExitCode int
}

type phaseCapture struct {
	buffer bytes.Buffer
	limit  int64
}

func (c *phaseCapture) Write(data []byte) (int, error) {
	remaining := c.limit - int64(c.buffer.Len())
	if int64(len(data)) <= remaining {
		return c.buffer.Write(data)
	}
	n, _ := c.buffer.Write(data[:remaining])
	return n, ErrPhaseOutputLimit
}
