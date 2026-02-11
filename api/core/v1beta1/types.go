package v1beta1

type DeviceCommand struct {
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
}

// This structure was copied from remotecommand.TerminalSize in order to avoid inclusion of the package by the
// agent
type TerminalSize struct {
	Width  uint16
	Height uint16
}

type DeviceConsoleSessionMetadata struct {
	Term              *string        `json:"term,omitempty"`
	InitialDimensions *TerminalSize  `json:"initialDimensions,omitempty"`
	Command           *DeviceCommand `json:"command,omitempty"`
	TTY               bool           `json:"tty,omitempty"`
	Protocols         []string       `json:"protocols,omitempty"`
}

type RolloutBatchCompletionReport struct {
	BatchName         string `json:"batchName"`
	SuccessPercentage int64  `json:"successPercentage"`
	Total             int64  `json:"total"`
	Successful        int64  `json:"successful"`
	Failed            int64  `json:"failed"`
	TimedOut          int64  `json:"timedOut"`
}

// A username on the system
type Username string

func (u Username) String() string {
	return string(u)
}

func (u Username) IsCurrentProcessUser() bool {
	return u == CurrentProcessUsername
}

const RootUsername Username = "root"

func (u Username) IsRootUser() bool {
	return u == RootUsername
}

func (u Username) WithDefault(fallback Username) Username {
	if u == "" {
		return fallback
	}
	return u
}

// The value to use as a Username when the user of the current process should be used (generally
// root).
const CurrentProcessUsername Username = ""

func (a ContainerApplication) RunAsWithDefault() Username {
	return a.RunAs.WithDefault(CurrentProcessUsername)
}

func (a QuadletApplication) RunAsWithDefault() Username {
	return a.RunAs.WithDefault(CurrentProcessUsername)
}

func (a ComposeApplication) RunAsWithDefault() Username {
	return CurrentProcessUsername
}
