package systeminfo

import (
	"context"
	stderrors "errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/config"
	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

type sourceKind int

const (
	systemInfoSource sourceKind = iota
	customInfoSource
	hiddenSource
)

type collector struct {
	source    *sourceDefinition
	collect   func(context.Context, *Info) error
	raw       *Info
	executors []*cachedExecutor
}

func (c *collector) pending() bool {
	return slices.ContainsFunc(c.executors, func(executor *cachedExecutor) bool {
		return !executor.attempted
	})
}

type sourceDefinition struct {
	collect collectorFunc
}

type collectorDefinition struct {
	source      *sourceDefinition
	extract     func(*Info) string
	projectInfo func(*Info, *Info)
}

type sourceEntry struct {
	key        string
	kind       sourceKind
	definition collectorDefinition
}

type customCollectionMode int

const (
	customCollectionDisabled customCollectionMode = iota
	customCollectionConfigured
	customCollectionDiscover
)

type collectionRequest struct {
	infoKeys []string
	custom   customCollectionRequest
}

type customCollectionRequest struct {
	mode customCollectionMode
	keys []string
}

type cachedExecutor struct {
	key         string
	kind        sourceKind
	extract     func(*Info) string
	projectInfo func(*Info, *Info)

	value              string
	hasValue           bool
	failed             bool
	message            string
	attempted          bool
	lastTransitionTime time.Time
}

type collectionError struct {
	message    string
	clearValue bool
}

func collectHostnameFunc(_ context.Context, _ *collectContext, info *Info) error {
	hostname, err := os.Hostname()
	info.Hostname = hostname
	return err
}

func collectArchitectureFunc(_ context.Context, _ *collectContext, info *Info) error {
	info.OperatingSystem = runtime.GOOS
	info.Architecture = runtime.GOARCH
	return nil
}

func collectCPUFunc(_ context.Context, collectCtx *collectContext, info *Info) error {
	cpuInfo, err := collectCPUInfo(collectCtx.reader)
	if err != nil {
		return fmt.Errorf("CPU collector failed: %w", err)
	}
	info.Hardware.CPU = cpuInfo
	return nil
}

func collectGPUFunc(_ context.Context, collectCtx *collectContext, info *Info) error {
	gpuInfo, err := collectGPUInfo(collectCtx.log, collectCtx.reader, collectCtx.hardwareMapFilePath)
	if err != nil {
		return fmt.Errorf("GPU collector failed: %w", err)
	}
	info.Hardware.GPU = gpuInfo
	return nil
}

func collectMemoryFunc(_ context.Context, collectCtx *collectContext, info *Info) error {
	memInfo, err := collectMemoryInfo(collectCtx.log, collectCtx.reader)
	if err != nil {
		return fmt.Errorf("memory collector failed: %w", err)
	}
	info.Hardware.Memory = memInfo
	return nil
}

func collectNetworkFunc(ctx context.Context, collectCtx *collectContext, info *Info) error {
	netInfo, err := collectNetworkInfo(ctx, collectCtx.log, collectCtx.exec, collectCtx.reader)
	if err != nil {
		return fmt.Errorf("network collector failed: %w", err)
	}
	info.Hardware.Network = netInfo
	return nil
}

func collectBIOSFunc(_ context.Context, collectCtx *collectContext, info *Info) error {
	biosInfo, err := collectBIOSInfo(collectCtx.reader)
	if err != nil {
		return fmt.Errorf("BIOS collector failed: %w", err)
	}
	info.Hardware.BIOS = biosInfo
	return nil
}

func collectSystemFunc(_ context.Context, collectCtx *collectContext, info *Info) error {
	systemInfo, err := collectSystemInfo(collectCtx.reader)
	if err != nil {
		return fmt.Errorf("system collector failed: %w", err)
	}
	info.Hardware.System = systemInfo
	return nil
}

func collectKernelFunc(ctx context.Context, collectCtx *collectContext, info *Info) error {
	out, err := collectCtx.exec.CommandContext(ctx, "uname", "-r").Output()
	if err != nil {
		return fmt.Errorf("kernel collector failed: %w", err)
	}
	info.Kernel = strings.TrimSpace(string(out))
	return nil
}

func collectDistributionFunc(ctx context.Context, collectCtx *collectContext, info *Info) error {
	distribution, err := collectDistributionInfo(ctx, collectCtx.reader)
	if err != nil {
		return fmt.Errorf("distribution collector failed: %w", err)
	}
	info.Distribution = distribution
	return nil
}

func collectBootFunc(ctx context.Context, collectCtx *collectContext, info *Info) error {
	bootID, err := getBootID(collectCtx.reader)
	if err != nil {
		return fmt.Errorf("boot collector failed to get boot ID: %w", err)
	}
	info.Boot.ID = bootID

	bootTime, err := getBootTime(ctx, collectCtx.exec)
	if err != nil {
		return fmt.Errorf("boot collector failed to get boot time: %w", err)
	}
	info.Boot.Time = bootTime
	return nil
}

// collectSystemInfo gathers system information
func collectSystemInfo(reader fileio.Reader) (*SystemInfo, error) {
	sysInfo := &SystemInfo{}

	fileFieldMap := map[string]*string{
		"sys_vendor":      &sysInfo.Manufacturer,
		"product_name":    &sysInfo.ProductName,
		"product_serial":  &sysInfo.SerialNumber, // requires root
		"product_uuid":    &sysInfo.UUID,         // requires root
		"product_version": &sysInfo.Version,
		"product_family":  &sysInfo.Family,
		"product_sku":     &sysInfo.SKU,
	}

	for fileName, fieldPtr := range fileFieldMap {
		filePath := filepath.Join(dmiClassPath, fileName)
		content, err := reader.ReadFile(filePath)
		if err == nil {
			*fieldPtr = strings.TrimSpace(string(content))
		}
		// best effort: ignore errors for missing files and permissions
	}

	return sysInfo, nil
}

// collectDistributionInfo gathers OS distribution information
func collectDistributionInfo(ctx context.Context, reader fileio.Reader) (map[string]interface{}, error) {
	distro := make(map[string]interface{})

	if _, err := os.Stat(reader.PathFor(osReleasePath)); err == nil {
		data, err := reader.ReadFile(osReleasePath)
		if err != nil {
			return nil, err
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}

				parts := strings.SplitN(line, "=", 2)
				if len(parts) != 2 {
					continue
				}

				key := parts[0]
				value := strings.Trim(parts[1], "\"")

				switch key {
				case "NAME":
					distro["name"] = value
				case "VERSION":
					distro["version"] = value
				case "ID":
					distro["id"] = value
				case "VERSION_ID":
					distro["version_id"] = value
				case "PRETTY_NAME":
					distro["pretty_name"] = value
				}
			}
		}
	}

	return distro, nil
}

// collectBIOSInfo gathers BIOS information
func collectBIOSInfo(reader fileio.Reader) (*BIOSInfo, error) {
	biosInfo := &BIOSInfo{}

	fileFieldMap := map[string]*string{
		"bios_vendor":  &biosInfo.Vendor,
		"bios_version": &biosInfo.Version,
		"bios_date":    &biosInfo.Date,
	}

	for fileName, fieldPtr := range fileFieldMap {
		filePath := filepath.Join(dmiClassPath, fileName)
		content, err := reader.ReadFile(filePath)
		if err != nil {
			// best effort: ignore errors for missing files and permissions
			continue
		}
		*fieldPtr = strings.TrimSpace(string(content))
	}

	if biosInfo.Vendor == "" && biosInfo.Version == "" && biosInfo.Date == "" {
		return nil, fmt.Errorf("unable to retrieve BIOS information")
	}

	return biosInfo, nil
}

func (e *collectionError) Error() string {
	return e.message
}

func (e *cachedExecutor) apply(info *Info, err error, now time.Time) {
	wasFailed := e.failed
	wasAttempted := e.attempted
	previousValue := e.value
	previousHasValue := e.hasValue
	e.attempted = true

	if err != nil {
		e.failed = true
		e.message = log.Truncate(collectionMessage(err), status.MaxMessageLength)
		var collectionErr *collectionError
		if stderrors.As(err, &collectionErr) && collectionErr.clearValue {
			e.value = ""
			e.hasValue = false
		}
	} else {
		e.value = e.extract(info)
		e.hasValue = true
		e.failed = false
		e.message = ""
	}

	valueChanged := previousHasValue != e.hasValue || previousValue != e.value
	if !wasAttempted || wasFailed != e.failed || valueChanged {
		e.lastTransitionTime = now.UTC().Truncate(time.Second)
	}
}

func (c *collector) apply(info *Info, err error, now time.Time) {
	var collectionErr *collectionError
	if err == nil || c.raw == nil || (stderrors.As(err, &collectionErr) && collectionErr.clearValue) {
		c.raw = info
	}
	for _, executor := range c.executors {
		executor.apply(info, err, now)
	}
}

func collectionMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func buildCollectors(
	logger *log.PrefixLogger,
	exec executer.Executer,
	reader fileio.Reader,
	hardwareMapPath string,
	request collectionRequest,
	runtimeCollectors map[string]CollectorFn,
	previous []*collector,
) []*collector {
	entries := []sourceEntry{bootSourceEntry()}
	for _, key := range request.infoKeys {
		if definition, ok := collectorForInfoKey(key); ok {
			entries = append(entries, sourceEntry{key: key, kind: systemInfoSource, definition: definition})
			continue
		}

		if fn, ok := runtimeCollectors[key]; ok {
			entries = append(entries, sourceEntry{key: key, kind: systemInfoSource, definition: runtimeDefinition(key, fn)})
		}
	}

	entries = append(entries, customEntries(logger, exec, reader, request.custom)...)
	return collectorsForEntries(logger, exec, reader, hardwareMapPath, entries, previous)
}

func collectorsForEntries(logger *log.PrefixLogger, exec executer.Executer, reader fileio.Reader, hardwareMapPath string, entries []sourceEntry, previous []*collector) []*collector {
	collectors := make([]*collector, 0, len(entries))
	for _, entry := range entries {
		if hasExecutor(collectors, entry.kind, entry.key) {
			continue
		}
		source := sourceForDefinition(collectors, entry.definition.source)
		if source == nil {
			source = newCollectorForSource(logger, exec, reader, hardwareMapPath, entry.definition.source)
			source.raw = cachedRawFor(previous, entry.kind, entry.key)
			collectors = append(collectors, source)
		}
		source.executors = append(source.executors, cachedExecutorFor(previous, entry))
	}
	return collectors
}

func newCollectorForSource(logger *log.PrefixLogger, exec executer.Executer, reader fileio.Reader, hardwareMapPath string, source *sourceDefinition) *collector {
	return &collector{
		source: source,
		collect: func(ctx context.Context, info *Info) error {
			return source.collect(ctx, &collectContext{
				log:                 logger,
				exec:                exec,
				reader:              reader,
				hardwareMapFilePath: hardwareMapPath,
			}, info)
		},
	}
}

func runtimeDefinition(key string, fn CollectorFn) collectorDefinition {
	return collectorDefinition{
		source: &sourceDefinition{collect: func(ctx context.Context, _ *collectContext, info *Info) error {
			if info.Custom == nil {
				info.Custom = make(map[string]string)
			}
			info.Custom[key] = sanitizeCollectorValue(fn(ctx))
			return nil
		}},
		extract:     func(info *Info) string { return info.Custom[key] },
		projectInfo: copyCustomInfo,
	}
}

var bootSource = &sourceDefinition{collect: collectBootFunc}

func bootSourceEntry() sourceEntry {
	return sourceEntry{
		kind: hiddenSource,
		definition: collectorDefinition{
			source:      bootSource,
			extract:     func(*Info) string { return "" },
			projectInfo: copyBootInfo,
		},
	}
}

func sourceForDefinition(collectors []*collector, definition *sourceDefinition) *collector {
	for _, source := range collectors {
		if source.source == definition {
			return source
		}
	}
	return nil
}

func customEntries(logger *log.PrefixLogger, exec executer.Executer, reader fileio.Reader, request customCollectionRequest) []sourceEntry {
	if request.mode == customCollectionDisabled {
		return nil
	}
	keys := request.keys
	scriptEntries, err := reader.ReadDir(config.SystemInfoCustomScriptDir)
	if err != nil {
		logger.Warningf("Failed to read custom system info scripts: %v", err)
		scriptEntries = nil
	}
	if request.mode == customCollectionDiscover {
		keys = discoverExecutableCustomInfoKeys(scriptEntries)
	}

	entries := make([]sourceEntry, 0, len(keys))
	for _, key := range keys {
		path, found := customScriptPath(key, reader, scriptEntries)
		entries = append(entries, sourceEntry{
			key:  key,
			kind: customInfoSource,
			definition: collectorDefinition{
				source: &sourceDefinition{collect: func(ctx context.Context, _ *collectContext, info *Info) error {
					return customCollector(exec, key, path, found)(ctx, info)
				}},
				extract:     func(info *Info) string { return info.Custom[key] },
				projectInfo: copyCustomInfo,
			},
		})
	}
	return entries
}

func customCollector(exec executer.Executer, key, path string, found bool) func(context.Context, *Info) error {
	return func(ctx context.Context, info *Info) error {
		if info.Custom == nil {
			info.Custom = make(map[string]string)
		}
		info.Custom[key] = ""
		if !found {
			return &collectionError{message: "script not found", clearValue: true}
		}
		stdout, stderr, exitCode := exec.ExecuteWithContext(ctx, path)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if exitCode != 0 {
			return deviceerrors.FromStderr(strings.TrimSpace(stderr), exitCode)
		}
		info.Custom[key] = strings.TrimSpace(stdout)
		return nil
	}
}

func customScriptPath(key string, reader fileio.Reader, entries []fs.DirEntry) (string, bool) {
	candidates := customScriptCandidates(key, entries)
	for _, name := range candidates {
		for _, entry := range entries {
			if entry.Name() != name || entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err == nil && info.Mode()&0111 != 0 {
				return filepath.Join(reader.PathFor(config.SystemInfoCustomScriptDir), name), true
			}
		}
	}
	return "", false
}

func customScriptCandidates(key string, entries []fs.DirEntry) []string {
	keyLower := strings.ToLower(key)
	candidates := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		base := strings.TrimSuffix(name, filepath.Ext(name))
		baseLower := strings.ToLower(base)
		if base == key || baseLower == keyLower || strings.HasSuffix(base, "-"+key) || strings.HasSuffix(baseLower, "-"+keyLower) {
			candidates = append(candidates, name)
		}
	}
	sort.Strings(candidates)
	return candidates
}

func discoverExecutableCustomInfoKeys(entries []fs.DirEntry) []string {
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Mode()&0111 == 0 {
			continue
		}
		key := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if key != "" && !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func copyBootInfo(destination, source *Info) {
	destination.Boot = source.Boot
}

func copyCustomInfo(destination, source *Info) {
	if destination.Custom == nil {
		destination.Custom = make(map[string]string)
	}
	maps.Copy(destination.Custom, source.Custom)
}

func cachedExecutorFor(previous []*collector, entry sourceEntry) *cachedExecutor {
	for _, source := range previous {
		for _, executor := range source.executors {
			if executor.kind == entry.kind && executor.key == entry.key {
				executor.extract = entry.definition.extract
				executor.projectInfo = entry.definition.projectInfo
				return executor
			}
		}
	}
	return &cachedExecutor{
		key:         entry.key,
		kind:        entry.kind,
		extract:     entry.definition.extract,
		projectInfo: entry.definition.projectInfo,
	}
}

func cachedRawFor(previous []*collector, kind sourceKind, key string) *Info {
	for _, source := range previous {
		for _, executor := range source.executors {
			if executor.kind == kind && executor.key == key {
				return source.raw
			}
		}
	}
	return nil
}

func hasExecutor(collectors []*collector, kind sourceKind, key string) bool {
	for _, source := range collectors {
		for _, executor := range source.executors {
			if executor.kind == kind && executor.key == key {
				return true
			}
		}
	}
	return false
}

func sanitizeCollectorValue(value string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune(" .:_/@+-", r) {
			return r
		}
		return -1
	}, strings.TrimSpace(value))
}
