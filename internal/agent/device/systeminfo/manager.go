package systeminfo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/version"
)

const (
	// DefaultBootIDPath is the path to the boot ID file.
	DefaultBootIDPath = "/proc/sys/kernel/random/boot_id"
	// SystemBootFileName is the name of the file where the system boot status is stored.
	SystemBootFileName = "system.json"
	// HardwareMapFileName is the name of the file where the hardware map is stored.
	HardwareMapFileName = "hardware-map.json"
)

type manager struct {
	bootID     string
	bootTime   string
	isRebooted bool

	exec              executer.Executer
	readWriter        fileio.ReadWriter
	dataDir           string
	factKeys          []string
	collectionTimeout time.Duration
	collected         bool

	log *log.PrefixLogger
}

func NewManager(
	log *log.PrefixLogger,
	exec executer.Executer,
	readWriter fileio.ReadWriter,
	dataDir string,
	factKeys []string,
	collectionTimeout util.Duration,
) *manager {
	return &manager{
		exec:              exec,
		readWriter:        readWriter,
		dataDir:           dataDir,
		factKeys:          factKeys,
		collectionTimeout: time.Duration(collectionTimeout),
		log:               log,
	}
}

func (m *manager) Initialize() (err error) {
	m.bootTime, err = getBootTime(m.exec)
	if err != nil {
		return err
	}
	m.bootID, err = getBootID(m.readWriter)
	if err != nil {
		return err
	}

	previousBoot, err := getBoot(m.readWriter, m.dataDir)
	if err != nil {
		return err
	}

	if !previousBoot.IsEmpty() && previousBoot.ID != m.bootID {
		m.isRebooted = true
	}

	// if we are rebooted or the previous status is empty, update the boot status on disk
	if m.isRebooted || previousBoot.IsEmpty() {
		// if we are rebooted, update the new boot status on disk
		systemBootPath := filepath.Join(m.dataDir, SystemBootFileName)
		boot := Boot{
			Time: m.bootTime,
			ID:   m.bootID,
		}
		bootBytes, err := json.Marshal(boot)
		if err != nil {
			return fmt.Errorf("marshalling system status: %w", err)
		}

		if err := m.readWriter.WriteFile(systemBootPath, bootBytes, 0644); err != nil {
			return fmt.Errorf("writing system status: %w", err)
		}
	}

	return nil
}

func (m *manager) IsRebooted() bool {
	return m.isRebooted
}

func (m *manager) BootID() string {
	return m.bootID
}

func (m *manager) BootTime() string {
	return m.bootTime
}

func (m *manager) Status(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	if m.collected {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, m.collectionTimeout)
	defer cancel()

	status.SystemInfo = collectDeviceSystemInfo(
		ctx,
		m.log,
		m.exec,
		m.readWriter,
		m.factKeys,
		m.bootID,
		filepath.Join(m.dataDir, HardwareMapFileName),
		m.dataDir,
	)

	m.collected = true

	return nil
}

func (m *manager) ReloadStatus() error {
	return nil
}

// collectDeviceSystemInfo collects the system information from the device and returns it as a DeviceSystemInfo object.
func collectDeviceSystemInfo(
	ctx context.Context,
	log *log.PrefixLogger,
	exec executer.Executer,
	reader fileio.Reader,
	factKeys []string,
	bootID string,
	hardwareMapPath string,
	dataDir string,
) v1alpha1.DeviceSystemInfo {
	agentVersion := version.Get()
	info, err := CollectInfo(ctx, log, exec, reader, hardwareMapPath)
	if err != nil {
		log.Errorf("failed to collect system info: %v", err)
	}

	facts := GenerateFacts(ctx, log, reader, exec, info, factKeys, dataDir)
	log.Tracef("system info facts: %v", facts)
	return v1alpha1.DeviceSystemInfo{
		Architecture:    runtime.GOARCH,
		OperatingSystem: runtime.GOOS,
		BootID:          bootID,
		AgentVersion:    agentVersion.GitVersion,
		// TODO: plumb in the facts
		// Facts:          facts,
	}
}

// getBoot returns the boot status from disk.
func getBoot(readWriter fileio.ReadWriter, dataDir string) (*Boot, error) {
	statusPath := filepath.Join(dataDir, SystemBootFileName)
	statusBytes, err := readWriter.ReadFile(statusPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// if the file does not exist, return an empty status
			return &Boot{}, nil
		}
		return nil, fmt.Errorf("reading boot status: %w", err)
	}

	var boot Boot
	if err := json.Unmarshal(statusBytes, &boot); err != nil {
		return nil, fmt.Errorf("unmarshal boot status: %w", err)
	}

	return &boot, nil
}

// returns the boot time as a string.
func getBootTime(exec executer.Executer) (string, error) {
	args := []string{"-s"}
	stdout, stderr, exitCode := exec.Execute("uptime", args...)
	if exitCode != 0 {
		return "", fmt.Errorf("device uptime: %w", errors.FromStderr(stderr, exitCode))
	}

	// parse boot time in local timezone since uptime -s returns timestamp in local time
	bootTime, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(stdout), time.Local)
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
			return "", nil
		}
		return "", err
	}

	return strings.TrimSpace(string(id)), nil
}
