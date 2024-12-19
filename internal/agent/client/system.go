package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
)

const (
	// DefaultBootIDPath is the path to the boot ID file.
	DefaultBootIDPath    = "/proc/sys/kernel/random/boot_id"
	SystemStatusFileName = "system.json"
)

type systemStatus struct {
	// BootTime is the time the system was booted.
	BootTime string `json:"bootTime"`
	// BootID is the unique boot ID populated by the kernel.
	BootID string `json:"bootID"`
}

func (s *systemStatus) IsEmpty() bool {
	return s.BootTime == "" && s.BootID == ""
}

type system struct {
	bootID     string
	bootTime   string
	isRebooted bool

	exec       executer.Executer
	readWriter fileio.ReadWriter
	dataDir    string
}

// NewSystem creates a new system client.
func NewSystem(exec executer.Executer, readWriter fileio.ReadWriter, dataDir string) System {
	return &system{
		exec:       exec,
		readWriter: readWriter,
		dataDir:    dataDir,
	}
}

func (b *system) Initialize() (err error) {
	b.bootTime, err = getBootTime(b.exec)
	if err != nil {
		return err
	}
	b.bootID, err = getBootID(b.readWriter)
	if err != nil {
		return err
	}

	previousStatus, err := getSystemStatus(b.readWriter, b.dataDir)
	if err != nil {
		return err
	}

	if !previousStatus.IsEmpty() && previousStatus.BootID != b.bootID {
		b.isRebooted = true
	}

	// if we are rebooted or the previous status is empty, update the boot status on disk
	if b.isRebooted || previousStatus.IsEmpty() {
		// if we are rebooted, update the new boot status on disk
		statusPath := filepath.Join(b.dataDir, SystemStatusFileName)
		systemStatus := systemStatus{
			BootTime: b.bootTime,
			BootID:   b.bootID,
		}
		bootBytes, err := json.Marshal(systemStatus)
		if err != nil {
			return fmt.Errorf("marshalling system status: %w", err)
		}

		if err := b.readWriter.WriteFile(statusPath, bootBytes, 0644); err != nil {
			return fmt.Errorf("writing system status: %w", err)
		}
	}

	return nil
}

func (b *system) IsRebooted() bool {
	return b.isRebooted
}

func (b *system) BootID() string {
	return b.bootID
}

func (b *system) BootTime() string {
	return b.bootTime
}

// status returns the boot status from disk.
func getSystemStatus(readWriter fileio.ReadWriter, dataDir string) (*systemStatus, error) {
	statusPath := filepath.Join(dataDir, SystemStatusFileName)
	statusBytes, err := readWriter.ReadFile(statusPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// if the file does not exist, return an empty status
			return &systemStatus{}, nil
		}
		return nil, fmt.Errorf("reading boot status: %w", err)
	}

	var bootStatus systemStatus
	if err := json.Unmarshal(statusBytes, &bootStatus); err != nil {
		return nil, fmt.Errorf("unmarshal boot status: %w", err)
	}

	return &bootStatus, nil
}

// returns the boot time as a string.
func getBootTime(exec executer.Executer) (string, error) {
	args := []string{"-s"}
	stdout, stderr, exitCode := exec.Execute("uptime", args...)
	if exitCode != 0 {
		return "", fmt.Errorf("device uptime: %w", errors.FromStderr(stderr, exitCode))
	}

	bootTime, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(stdout))
	if err != nil {
		return "", err
	}

	return bootTime.UTC().Format(time.RFC3339), nil
}

// returns the boot ID. If the boot ID file is not found it returns unknown.
func getBootID(reader fileio.Reader) (string, error) {
	id, err := reader.ReadFile(DefaultBootIDPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// if the file does not exist, return empty string this is the case
			// for non-linux systems and integration/simulation tests
			return "", nil
		}
		return "", err
	}

	return string(id), nil
}
