package systeminfo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	status "github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/version"
	"github.com/samber/lo"
)

type manager struct {
	bootID     string
	bootTime   string
	isRebooted bool

	exec       executer.Executer
	readWriter fileio.ReadWriter
	dataDir    string

	mu                sync.Mutex
	infoKeys          []string
	customKeys        []string
	collectionTimeout time.Duration
	collectors        map[string]CollectorFn
	collected         bool

	log *log.PrefixLogger
}

func NewManager(
	log *log.PrefixLogger,
	exec executer.Executer,
	readWriter fileio.ReadWriter,
	dataDir string,
	infoKeys []string,
	customKeys []string,
	collectionTimeout util.Duration,
) *manager {
	return &manager{
		exec:              exec,
		readWriter:        readWriter,
		dataDir:           dataDir,
		infoKeys:          infoKeys,
		customKeys:        customKeys,
		collectionTimeout: time.Duration(collectionTimeout),
		collectors:        make(map[string]CollectorFn),
		log:               log,
	}
}

func (m *manager) Initialize(ctx context.Context) (err error) {
	m.bootTime, err = getBootTime(ctx, m.exec)
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
		systemBootPath := filepath.Join(m.dataDir, SystemFileName)
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

// ReloadConfig reloads the system info from the agent config.
func (m *manager) ReloadConfig(ctx context.Context, cfg *config.Config) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Info("Reloading system info config")

	if !reflect.DeepEqual(m.infoKeys, cfg.SystemInfo) {
		m.log.Infof("Updating system info keys: %v -> %v", m.infoKeys, cfg.SystemInfo)
		m.infoKeys = cfg.SystemInfo
	}

	if !reflect.DeepEqual(m.customKeys, cfg.SystemInfoCustom) {
		m.log.Infof("Updating custom system info keys: %v -> %v", m.customKeys, cfg.SystemInfoCustom)
		m.customKeys = cfg.SystemInfoCustom
	}

	timeout := time.Duration(cfg.SystemInfoTimeout)
	if m.collectionTimeout != timeout {
		m.log.Infof("Updating system info collection timeout: %v -> %v", m.collectionTimeout, timeout)
		m.collectionTimeout = timeout
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

func (m *manager) Status(ctx context.Context, deviceStatus *v1alpha1.DeviceStatus, opts ...status.CollectorOpt) error {
	collectorOpts := status.CollectorOpts{}
	for _, opt := range opts {
		opt(&collectorOpts)
	}
	m.mu.Lock()

	if m.collected && !collectorOpts.Force {
		m.mu.Unlock()
		return nil
	}

	// set collected to true even if there is an error this is to prevent
	// collecting system info multiple times
	m.collected = true

	// reduce scope of the mutex
	timeout := m.collectionTimeout
	infoKeys := slices.Clone(m.infoKeys)
	customKeys := slices.Clone(m.customKeys)
	bootID := m.bootID
	collectors := make(map[string]CollectorFn, len(m.collectors))
	for k, v := range m.collectors {
		collectors[k] = v
	}
	dataDir := m.dataDir
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	systemInfo, err := collectDeviceSystemInfo(
		ctx,
		m.log,
		m.exec,
		m.readWriter,
		infoKeys,
		customKeys,
		bootID,
		collectors,
		filepath.Join(dataDir, HardwareMapFileName),
	)

	if err != nil {
		deviceStatus.SystemInfo = m.defaultSystemInfo()
		return err
	}
	deviceStatus.SystemInfo = systemInfo

	return nil
}

// defaultSystemInfo returns the default system info.
func (m *manager) defaultSystemInfo() v1alpha1.DeviceSystemInfo {
	return v1alpha1.DeviceSystemInfo{
		BootID:               m.bootID,
		AgentVersion:         version.Get().String(),
		OperatingSystem:      runtime.GOOS,
		Architecture:         runtime.GOARCH,
		AdditionalProperties: make(map[string]string),
	}
}

// RegisterCollector allows the caller to register a collector function for system information.
func (m *manager) RegisterCollector(ctx context.Context, key string, fn CollectorFn) {
	m.log.Debugf("Registering system info collector: %s", key)
	if _, ok := m.collectors[key]; ok {
		m.log.Errorf("Collector %s already registered", key)
		return
	}
	m.collectors[key] = fn
}

// collectDeviceSystemInfo collects the system information from the device and returns it as a DeviceSystemInfo object.
func collectDeviceSystemInfo(
	ctx context.Context,
	log *log.PrefixLogger,
	exec executer.Executer,
	reader fileio.Reader,
	infoKeys []string,
	customKeys []string,
	bootID string,
	collectors map[string]CollectorFn,
	hardwareMapPath string,
) (v1alpha1.DeviceSystemInfo, error) {
	agentVersion := version.Get()
	info, err := Collect(ctx, log, exec, reader, customKeys, hardwareMapPath)
	if err != nil {
		log.Errorf("Failed to collect system info: %v", err)
		return v1alpha1.DeviceSystemInfo{}, err
	}

	systemInfoMap := getSystemInfoMap(ctx, log, info, infoKeys, collectors)
	log.Tracef("system info map: %v", systemInfoMap)
	s := v1alpha1.DeviceSystemInfo{
		Architecture:         info.Architecture,
		OperatingSystem:      info.OperatingSystem,
		BootID:               bootID,
		AgentVersion:         agentVersion.GitVersion,
		AdditionalProperties: systemInfoMap,
	}
	if len(info.Custom) > 0 {
		s.CustomInfo = lo.ToPtr(v1alpha1.CustomDeviceInfo(info.Custom))
	}
	return s, nil
}

// getBoot returns the boot status from disk.
func getBoot(readWriter fileio.ReadWriter, dataDir string) (*Boot, error) {
	statusPath := filepath.Join(dataDir, SystemFileName)
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
func getBootTime(ctx context.Context, exec executer.Executer) (string, error) {
	args := []string{"-s"}
	stdout, stderr, exitCode := exec.ExecuteWithContext(ctx, "uptime", args...)
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
	id, err := reader.ReadFile(bootIDPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}

	return strings.TrimSpace(string(id)), nil
}
