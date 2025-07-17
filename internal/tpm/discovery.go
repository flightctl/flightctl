package tpm

import (
	"bytes"
	"fmt"
	"regexp"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	devicePathTemplate   = "/dev/tpm%s"
	deviceRMPathTemplate = "/dev/tpmrm%s"
	versionPathTemplate  = "/sys/class/tpm/%s/tpm_version_major"
	sysClassPath         = "/sys/class/tpm"
	sysFsPathTemplate    = "/sys/class/tpm/%s"
)

// tpmIndexRegex matches explicitly tpm (not tpmrm!) and captures the tpm's index
var tpmIndexRegex = regexp.MustCompile(`^tpm(\d+)$`)

func discoverTPMs(rw fileio.ReadWriter) ([]TPM, error) {
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
		deviceNum := matches[1]

		device := TPM{
			index:           deviceNum,
			path:            fmt.Sprintf(devicePathTemplate, deviceNum),
			resourceMgrPath: fmt.Sprintf(deviceRMPathTemplate, deviceNum),
			versionPath:     fmt.Sprintf(versionPathTemplate, entry.Name()),
			sysfsPath:       fmt.Sprintf(sysFsPathTemplate, entry.Name()),
			rw:              rw,
		}

		tpms = append(tpms, device)
	}

	return tpms, nil
}

// ResolveTPM returns the TPM specified by the path if it exists and if the specified device is version 2
func ResolveTPM(rw fileio.ReadWriter, path string) (*TPM, error) {
	tpms, err := discoverTPMs(rw)
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

// ResolveDefaultTPM finds and returns the first available valid TPM 2.0.
func ResolveDefaultTPM(rw fileio.ReadWriter, logger *log.PrefixLogger) (*TPM, error) {
	tpms, err := discoverTPMs(rw)
	if err != nil {
		return nil, fmt.Errorf("failed to discover TPMs: %w", err)
	}

	logger.Debugf("Found %d TPMs", len(tpms))

	for _, tpm := range tpms {
		logger.Debugf("Trying TPM %q at %q", tpm.index, tpm.resourceMgrPath)
		if tpm.Exists() {
			logger.Debugf("Device %q exists, validating version", tpm.index)
			if err := tpm.ValidateVersion2(); err == nil {
				return &tpm, nil
			}
			logger.Debugf("Device %q validation failed: %v", tpm.index, err)
		} else {
			logger.Debugf("Device %q does not exist", tpm.index)
		}
	}

	return nil, fmt.Errorf("no valid TPM 2.0 devices found")
}

func (d *TPM) Exists() bool {
	exists, err := d.rw.PathExists(d.resourceMgrPath, fileio.WithSkipContentCheck())
	return err == nil && exists
}

func (d *TPM) ValidateVersion2() error {
	if !d.Exists() {
		return fmt.Errorf("no TPM detected at %s", d.resourceMgrPath)
	}
	versionBytes, err := d.rw.ReadFile(d.versionPath)
	if err != nil {
		return fmt.Errorf("reading tpm version file: %w", err)
	}
	versionStr := string(bytes.TrimSpace(versionBytes))
	if versionStr != "2" {
		return fmt.Errorf("TPM is not version 2.0. Found version: %s", versionStr)
	}
	return nil
}

func (d *TPM) Open(lDevIdPersistencePath string) (*Client, error) {
	if d.client != nil {
		return d.client, nil
	}

	tpm, err := CreateClient(d.rw, d.resourceMgrPath, lDevIdPersistencePath)
	if err != nil {
		return nil, fmt.Errorf("creating client: %w", err)
	}
	d.client = tpm
	return tpm, nil
}

func (d *TPM) Close() error {
	if d.client == nil {
		return nil
	}
	err := d.client.Close()
	d.client = nil
	return err
}
