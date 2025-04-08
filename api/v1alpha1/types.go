package v1alpha1

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
