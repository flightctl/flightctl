package executer

import (
	"bytes"
	"errors"
	"os"
	"time"

	"github.com/flightctl/flightctl/pkg/phasebootstrap"
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

// PhaseNamespaceID identifies one namespace by its procfs device and inode.
type PhaseNamespaceID = phasebootstrap.NamespaceID

// PhaseNamespaceIdentity records the system-manager host namespace baseline.
type PhaseNamespaceIdentity = phasebootstrap.Identity

// DefaultPhaseLauncherPath names the small static launcher installed with the
// agent package.
const DefaultPhaseLauncherPath = "/usr/libexec/flightctl/phase-launcher"

// PhaseExecSpec describes a caller-owned, verified execution root kept stable
// until cleanup. Args excludes argv[0]. All paths inside the root are agent-owned
// inputs except the optional ephemeral /work mount. SourceFD is a read-only pipe
// inherited as stdin for one admitted prepare source. LauncherPath is a trusted
// static host executable staged into the root; it proves namespaces and then
// replaces itself with the updater under the same systemd invocation.
type PhaseExecSpec struct {
	RootDir        string
	Entrypoint     string
	Args           []string
	WritableWork   bool
	SourceFD       *os.File
	LauncherPath   string
	HostNamespaces PhaseNamespaceIdentity
	Limits         PhaseExecLimits
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
