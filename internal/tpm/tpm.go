package tpm

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/go-tpm/tpm2"
)

const (
	MinNonceLength = 8

	// TPM 2.0 Capability Constants (TPM 2.0 Specification Part 2, Rev 1.65, Section 6.5)
	// TPMCapTPMProperties represents capability for TPM properties
	TPMCapTPMProperties = 0x00000006
	// TPMPTFamilyIndicator represents the TPM family indicator property
	TPMPTFamilyIndicator = 0x0100

	// NVRAM Certificate Wrapper Constants (TCG PC Client Platform TPM Profile v1.05 Rev 14, §7.3.2)
	// NVRAMCertPrefixLength is the length of the NVRAM certificate prefix
	NVRAMCertPrefixLength = 3
	// NVRAMCertHeaderLength is the total length of the NVRAM certificate header
	NVRAMCertHeaderLength = 5

	// TPM Handle Ranges
	// PersistentHandleMin is the minimum valid persistent handle value.
	persistentHandleMin = tpm2.TPMHandle(0x81000000)
	// PersistentHandleMax is the maximum valid persistent handle value.
	persistentHandleMax = tpm2.TPMHandle(0x81FFFFFF)

	tpmPathTemplate     = "/dev/tpm%s"
	rmPathTemplate      = "/dev/tpmrm%s"
	versionPathTemplate = "/sys/class/tpm/%s/tpm_version_major"
	sysClassPath        = "/sys/class/tpm"
	sysFsPathTemplate   = "/sys/class/tpm/%s"
)

var (
	// NVRAMCertPrefix is the 3-byte prefix for certificates stored in TPM NVRAM
	// Per TCG PC Client Platform TPM Profile v1.05 Rev 14, Section 7.3.2
	NVRAMCertPrefix = []byte{0x10, 0x01, 0x00}
)

// TPM represents a TPM device and its associated file paths.
type TPM struct {
	// index is the numeric identifier of the TPM device (e.g., "0" for /dev/tpm0).
	index string
	// path is the full path to the TPM device file (e.g., "/dev/tpm0").
	path string
	// resourceMgrPath is the path to the TPM resource manager (e.g., "/dev/tpmrm0").
	resourceMgrPath string
	// versionPath is the path to the TPM version file in sysfs.
	versionPath string
	// sysfsPath is the path to the TPM device directory in sysfs.
	sysfsPath string
	client    *Client
	rw        fileio.ReadWriter
}

func (t *TPM) Exists() bool {
	exists, err := t.rw.PathExists(t.resourceMgrPath, fileio.WithSkipContentCheck())
	return err == nil && exists
}

func (t *TPM) ValidateVersion2() error {
	if !t.Exists() {
		return fmt.Errorf("no TPM detected at %s", t.resourceMgrPath)
	}
	versionBytes, err := t.rw.ReadFile(t.versionPath)
	if err != nil {
		return fmt.Errorf("reading tpm version file: %w", err)
	}
	versionStr := string(bytes.TrimSpace(versionBytes))
	if versionStr != "2" {
		return fmt.Errorf("TPM is not version 2.0. Found version: %s (FlightCtl requires TPM 2.0)", versionStr)
	}
	return nil
}

func (t *TPM) Close(ctx context.Context) error {
	if t.client == nil {
		return nil
	}
	err := t.client.Close(ctx)
	t.client = nil
	return err
}

// tpmIndexRegex matches explicitly tpm (not tpmrm!) and captures the tpm's index
var tpmIndexRegex = regexp.MustCompile(`^tpm(\d+)$`)

func resolveFromPath(rw fileio.ReadWriter, log *log.PrefixLogger, path string) (*TPM, error) {
	if path == "" {
		log.Infof("No TPM device provided. Selecting a default device")
		return resolveDefault(rw, log)
	}
	log.Infof("Using TPM device at %s", path)
	return resolve(rw, path)
}

// resolve returns the TPM specified by the path if it exists and if the specified device is version 2
func resolve(rw fileio.ReadWriter, path string) (*TPM, error) {
	tpms, err := discover(rw)
	if err != nil {
		return nil, fmt.Errorf("discovering TPM devices: %w", err)
	}

	for _, tpm := range tpms {
		if tpm.path == path || tpm.resourceMgrPath == path {
			if err := tpm.ValidateVersion2(); err != nil {
				return nil, fmt.Errorf("invalid TPM %q: %w", path, err)
			}
			return &tpm, nil
		}
	}

	return nil, fmt.Errorf("TPM %q not found", path)
}

// resolveDefault finds and returns the first available valid TPM 2.0.
func resolveDefault(rw fileio.ReadWriter, logger *log.PrefixLogger) (*TPM, error) {
	tpms, err := discover(rw)
	if err != nil {
		return nil, fmt.Errorf("failed to discover TPMs: %w", err)
	}

	logger.Debugf("Found %d TPMs", len(tpms))

	var versionErrors []string
	for _, tpm := range tpms {
		logger.Debugf("Trying TPM %q at %q", tpm.index, tpm.resourceMgrPath)
		if tpm.Exists() {
			logger.Debugf("Device %q exists, validating version", tpm.index)
			if err := tpm.ValidateVersion2(); err == nil {
				return &tpm, nil
			} else {
				logger.Debugf("Device %q validation failed: %v", tpm.index, err)
				versionErrors = append(versionErrors, fmt.Sprintf("TPM %s: %v", tpm.index, err))
			}
		} else {
			logger.Debugf("Device %q does not exist", tpm.index)
		}
	}

	if len(versionErrors) > 0 {
		logger.Warnf("Found TPM devices but none are version 2.0: %v", versionErrors)
		return nil, fmt.Errorf("no valid TPM 2.0 devices found (detected: %s)", strings.Join(versionErrors, ", "))
	}

	return nil, fmt.Errorf("no valid TPM 2.0 devices found")
}

func discover(rw fileio.ReadWriter) ([]TPM, error) {
	entries, err := rw.ReadDir(sysClassPath)
	if err != nil {
		return nil, fmt.Errorf("scanning TPM devices: %w", err)
	}

	var tpms []TPM
	for _, entry := range entries {
		matches := tpmIndexRegex.FindStringSubmatch(entry.Name())
		if len(matches) != 2 {
			continue
		}
		index := matches[1]

		tpm := TPM{
			index:           index,
			path:            fmt.Sprintf(tpmPathTemplate, index),
			resourceMgrPath: fmt.Sprintf(rmPathTemplate, index),
			versionPath:     fmt.Sprintf(versionPathTemplate, entry.Name()),
			sysfsPath:       fmt.Sprintf(sysFsPathTemplate, entry.Name()),
			rw:              rw,
		}

		tpms = append(tpms, tpm)
	}

	return tpms, nil
}
