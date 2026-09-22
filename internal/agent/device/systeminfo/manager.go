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

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo/common"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/version"
)

type manager struct {
	bootID     string
	bootTime   string
	isRebooted bool

	exec       executer.Executer
	readWriter fileio.ReadWriter
	dataDir    string

	mu                 sync.Mutex
	collectionMu       sync.Mutex
	infoKeys           []string
	customKeys         []string
	collectionTimeout  time.Duration
	collectionInterval time.Duration
	collectionChanged  chan struct{}
	runtimeCollectors  map[string]CollectorFn
	collection         []*collector
	now                func() time.Time

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
	collectionInterval util.Duration,
) *manager {
	m := &manager{
		exec:               exec,
		readWriter:         readWriter,
		dataDir:            dataDir,
		infoKeys:           infoKeys,
		customKeys:         customKeys,
		collectionTimeout:  time.Duration(collectionTimeout),
		collectionInterval: time.Duration(collectionInterval),
		collectionChanged:  make(chan struct{}, 1),
		runtimeCollectors:  make(map[string]CollectorFn),
		now:                time.Now,
		log:                log,
	}
	m.rebuildCollectors()
	return m
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

	m.collect(ctx)

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

	selectionChanged := !reflect.DeepEqual(m.infoKeys, cfg.SystemInfo) || !reflect.DeepEqual(m.customKeys, cfg.SystemInfoCustom)
	if selectionChanged {
		m.log.Infof("Updating system info keys: %v -> %v", m.infoKeys, cfg.SystemInfo)
		m.log.Infof("Updating custom system info keys: %v -> %v", m.customKeys, cfg.SystemInfoCustom)
		m.infoKeys = cfg.SystemInfo
		m.customKeys = cfg.SystemInfoCustom
	}
	// Reload custom script definitions on every SIGHUP, even when the
	// configured keys are unchanged.
	m.rebuildCollectors()

	timeout := time.Duration(cfg.SystemInfoTimeout)
	if m.collectionTimeout != timeout {
		m.log.Infof("Updating system info collection timeout: %v -> %v", m.collectionTimeout, timeout)
		m.collectionTimeout = timeout
	}

	collectionChanged := selectionChanged
	interval := time.Duration(cfg.SystemInfoCollectionInterval())
	if m.collectionInterval != interval {
		m.log.Infof("Updating system info collection interval: %v -> %v", m.collectionInterval, interval)
		m.collectionInterval = interval
		collectionChanged = true
	}
	if collectionChanged {
		select {
		case m.collectionChanged <- struct{}{}:
		default:
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

func (m *manager) Status(ctx context.Context, deviceStatus *v1beta1.DeviceStatus, opts ...status.CollectorOpt) error {
	collectorOpts := status.CollectorOpts{}
	for _, opt := range opts {
		opt(&collectorOpts)
	}
	if collectorOpts.Force {
		m.collect(ctx)
	}
	deviceStatus.SystemInfo, deviceStatus.SystemInfoStatus = m.systemInfoFromCache()

	return nil
}

// Run starts periodic system info collection and stops when ctx is cancelled.
func (m *manager) Run(ctx context.Context) {
	var ticker *time.Ticker
	var ticks <-chan time.Time
	resetTicker := func(interval time.Duration) {
		if ticker != nil {
			ticker.Stop()
			ticker = nil
			ticks = nil
		}
		if interval > 0 {
			ticker = time.NewTicker(interval)
			ticks = ticker.C
		}
	}
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()

	m.mu.Lock()
	resetTicker(m.collectionInterval)
	m.mu.Unlock()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.collectionChanged:
			m.mu.Lock()
			resetTicker(m.collectionInterval)
			m.mu.Unlock()
			m.collect(ctx)
		case <-ticks:
			m.collect(ctx)
		}
	}
}

func (m *manager) rebuildCollectors() {
	m.collection = buildCollectors(
		m.log,
		m.exec,
		m.readWriter,
		filepath.Join(m.dataDir, HardwareMapFileName),
		managerCollectionRequest(m.infoKeys, m.customKeys),
		m.runtimeCollectors,
		m.collection,
	)
}

func managerCollectionRequest(infoKeys, customKeys []string) collectionRequest {
	custom := customCollectionRequest{mode: customCollectionDisabled}
	if customKeys == nil {
		custom.mode = customCollectionDiscover
	} else if len(customKeys) > 0 {
		custom = customCollectionRequest{mode: customCollectionConfigured, keys: customKeys}
	}
	return collectionRequest{infoKeys: infoKeys, custom: custom}
}

func (m *manager) collect(ctx context.Context) {
	m.collectConfigured(ctx, false)
}

// RefreshPendingCollectors collects sources that have not yet been collected.
func (m *manager) RefreshPendingCollectors(ctx context.Context) {
	m.collectConfigured(ctx, true)
}

func (m *manager) collectConfigured(ctx context.Context, pendingOnly bool) {
	m.collectionMu.Lock()
	defer m.collectionMu.Unlock()

	m.mu.Lock()
	timeout := m.collectionTimeout
	sources := slices.Clone(m.collection)
	if pendingOnly {
		sources = slices.DeleteFunc(sources, func(source *collector) bool {
			return !source.pending()
		})
	}
	m.mu.Unlock()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	m.collectSources(ctx, sources)
}

func (m *manager) collectSources(ctx context.Context, sources []*collector) {
	for _, source := range sources {
		if ctx.Err() != nil {
			return
		}
		info := &Info{Hardware: HardwareFacts{}}
		err := source.collect(ctx, info)
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil && !errors.IsContext(err) {
			m.log.Warningf("System info collector failed: %v", err)
		}
		m.mu.Lock()
		source.apply(info, err, m.now())
		m.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
	}
}

func (m *manager) infoFromCache() *Info {
	m.mu.Lock()
	defer m.mu.Unlock()

	info := &Info{
		CollectedAt: time.Now().Format(time.RFC3339),
		Hardware:    HardwareFacts{},
		Metadata: map[string]interface{}{
			"collector_version": version.Get().String(),
			"collector_type":    "flightctl-agent",
		},
	}
	for _, source := range m.collection {
		if source.raw == nil {
			continue
		}
		for _, executor := range source.executors {
			if executor.projectInfo != nil {
				executor.projectInfo(info, source.raw)
			}
		}
	}
	return info
}

func (m *manager) systemInfoFromCache() (v1beta1.DeviceSystemInfo, *v1beta1.DeviceSystemInfoStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()

	systemInfo := m.defaultSystemInfo()
	systemInfo.AdditionalProperties = make(map[string]string)
	customInfo := make(v1beta1.CustomDeviceInfo)
	statuses := v1beta1.DeviceSystemInfoStatuses{
		SystemInfo: make(map[string]v1beta1.SystemInfoSourceStatus),
		CustomInfo: make(map[string]v1beta1.SystemInfoSourceStatus),
	}
	failed := 0
	total := 0
	unknown := false
	for _, source := range m.collection {
		for _, exec := range source.executors {
			if exec.kind == hiddenSource {
				continue
			}
			total++
			if !exec.attempted {
				unknown = true
				continue
			}
			entry := v1beta1.SystemInfoSourceStatus{
				LastTransitionTime: exec.lastTransitionTime,
				Status:             v1beta1.SystemInfoSourceStatusHealthy,
			}
			if exec.failed {
				entry.Message = new(exec.message)
				entry.Status = v1beta1.SystemInfoSourceStatusError
				failed++
			}
			if exec.kind == systemInfoSource {
				statuses.SystemInfo[exec.key] = entry
				if exec.hasValue {
					systemInfo.AdditionalProperties[exec.key] = exec.value
				}
			} else {
				statuses.CustomInfo[exec.key] = entry
				if exec.hasValue {
					customInfo[exec.key] = exec.value
				}
			}
		}
	}
	if len(customInfo) > 0 {
		systemInfo.CustomInfo = &customInfo
	}

	summary := v1beta1.SystemInfoSummaryStatusHealthy
	if total == 0 || unknown {
		summary = v1beta1.SystemInfoSummaryStatusUnknown
	} else if failed == total {
		summary = v1beta1.SystemInfoSummaryStatusError
	} else if failed > 0 {
		summary = v1beta1.SystemInfoSummaryStatusDegraded
	}
	return systemInfo, &v1beta1.DeviceSystemInfoStatus{
		Statuses: statuses,
		Summary:  v1beta1.DeviceSystemInfoSummaryStatus{Status: summary},
	}
}

// defaultSystemInfo returns the default system info.
func (m *manager) defaultSystemInfo() v1beta1.DeviceSystemInfo {
	return v1beta1.DeviceSystemInfo{
		BootID:               m.bootID,
		AgentVersion:         version.Get().String(),
		OperatingSystem:      runtime.GOOS,
		Architecture:         runtime.GOARCH,
		AdditionalProperties: make(map[string]string),
	}
}

// RegisterCollector allows the caller to register a collector function for system information.
func (m *manager) RegisterCollector(ctx context.Context, key string, fn CollectorFn) {
	if ctx != nil && ctx.Err() != nil {
		return
	}
	if key == "" || fn == nil {
		if m.log != nil {
			m.log.Errorf("Invalid system info collector registration (key=%q, fn=nil=%t)", key, fn == nil)
		}
		return
	}

	if !common.IsRuntimeKey(key) {
		if m.log != nil {
			m.log.Errorf("Unknown system info collector key: %q", key)
		}
		return
	}

	if _, isBuiltIn := collectorForInfoKey(key); isBuiltIn {
		if m.log != nil {
			m.log.Errorf("BuiltIn system info key must not be registered as a runtime collector: %q", key)
		}
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.log != nil {
		m.log.Debugf("Registering system info collector: %s", key)
	}

	if _, ok := m.runtimeCollectors[key]; ok {
		if m.log != nil {
			m.log.Errorf("Collector %s already registered", key)
		}
		return
	}

	m.runtimeCollectors[key] = fn
	if slices.Contains(m.infoKeys, key) {
		m.rebuildCollectors()
	}
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
