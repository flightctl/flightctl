package v1beta1

import (
	"encoding/base64"
	"fmt"
)

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

// decodeContents decodes the content based on the encoding type and returns the
// decoded content as a byte slice.
func decodeContents(content string, encoding *EncodingType) ([]byte,
	error) {
	if encoding == nil || *encoding == "plain" {
		return []byte(content), nil
	}

	switch *encoding {
	case "base64":
		decoded, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return nil, fmt.Errorf("failed to decode base64 content: %w", err)
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("unsupported content encoding: %q", *encoding)
	}
}

func (f *FileSpec) ContentsDecoded() ([]byte, error) {
	return decodeContents(f.Content, f.ContentEncoding)
}

func (a *ApplicationContent) ContentsDecoded() ([]byte, error) {
	if a.Content == nil {
		return nil, nil
	}
	return decodeContents(*a.Content, a.ContentEncoding)
}
