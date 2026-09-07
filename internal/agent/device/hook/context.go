package hook

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"k8s.io/client-go/util/cert"
)

const (
	// HookContextDir is the runtime directory for hook context files.
	HookContextDir = "/run/flightctl"
	// HookContextFile is the filename for the hook context JSON.
	HookContextFile = "hook-context.json"
	// HookContextPath is the full path to the hook context JSON file.
	HookContextPath = "/run/flightctl/hook-context.json"
	// HookContextMode is the file permission for the hook context file (owner read/write only).
	HookContextMode = 0600
)

// HookLabelsPath is the well-known file where pre-enrollment hooks write labels.
const HookLabelsPath = "/run/flightctl/hook-labels.json"

// EnrollmentContext carries device metadata for enrollment hook execution.
// Callers populate input fields before invoking OnBeforeEnrolling/OnAfterEnrolling.
// Result fields are populated by the hook manager after execution.
type EnrollmentContext struct {
	// Input fields (set by caller before OnBeforeEnrolling)
	DeviceName            string
	SystemInfo            map[string]interface{}
	Labels                map[string]string
	ManagementCertificate *CertificateMetadata

	// Result fields (populated by OnBeforeEnrolling)
	Success    bool
	Output     string
	HookLabels map[string]string
}

// CertificateMetadata contains management certificate identity info (no private key).
type CertificateMetadata struct {
	// SerialNumber is the certificate serial number as colon-separated uppercase hex
	// (e.g. "46:E5:42:26:2B:79:34:F2").
	SerialNumber string `json:"serialNumber"`
	// NotAfter is the certificate expiry time formatted as RFC 3339
	// (e.g. "2027-09-06T00:00:00Z"). Required by design §4.2.
	NotAfter string `json:"notAfter"`
	// SHA256 is the lowercase hex SHA-256 fingerprint of the DER-encoded certificate.
	SHA256 string `json:"sha256"`
}

// hookContext is the JSON structure written to HookContextPath.
// Unexported: only the hook manager writes it.
type hookContext struct {
	Hook                  string                 `json:"hook"`
	DeviceName            string                 `json:"deviceName"`
	SystemInfo            map[string]interface{} `json:"systemInfo"`
	Labels                map[string]string      `json:"labels"`
	ManagementCertificate *CertificateMetadata   `json:"managementCertificate"`
}

// ParseCertMetadata extracts identity metadata from a PEM-encoded certificate.
// Returns nil, nil for nil/empty input (pre-enrollment case).
// Returns error for malformed PEM.
//
// String formats:
//   - serialNumber: colon-separated uppercase hex (e.g. "46:E5:42:26:2B:79:34:F2")
//   - notAfter: RFC 3339 via time.RFC3339 (e.g. "2027-09-06T00:00:00Z")
//   - sha256: lowercase hex fingerprint of DER bytes
func ParseCertMetadata(pemData []byte) (*CertificateMetadata, error) {
	if len(pemData) == 0 {
		return nil, nil
	}

	certs, err := cert.ParseCertsPEM(pemData)
	if err != nil {
		return nil, fmt.Errorf("parsing certificate PEM: %w", err)
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("no certificates found in PEM data")
	}

	c := certs[0]

	return &CertificateMetadata{
		SerialNumber: formatSerialNumber(c.SerialNumber.Bytes()),
		NotAfter:     c.NotAfter.Format("2006-01-02T15:04:05Z07:00"), // time.RFC3339
		SHA256:       fmt.Sprintf("%x", sha256.Sum256(c.Raw)),
	}, nil
}

// formatSerialNumber converts raw serial number bytes to colon-separated
// uppercase hex (e.g. "46:E5:42:26:2B:79:34:F2").
func formatSerialNumber(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	out := make([]byte, 0, len(b)*3-1)
	for i, v := range b {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, fmt.Sprintf("%02X", v)...)
	}
	return string(out)
}

// writeHookContext marshals enrollment context to JSON, writes it to
// HookContextPath with mode 0600, and returns the JSON bytes for use
// as the FLIGHTCTL_HOOK_CONTEXT environment variable value.
func writeHookContext(readWriter fileio.ReadWriter, hookType string, enrollCtx *EnrollmentContext) ([]byte, error) {
	ctx := hookContext{
		Hook:                  hookType,
		DeviceName:            enrollCtx.DeviceName,
		SystemInfo:            enrollCtx.SystemInfo,
		Labels:                enrollCtx.Labels,
		ManagementCertificate: enrollCtx.ManagementCertificate,
	}

	jsonBytes, err := json.Marshal(ctx)
	if err != nil {
		return nil, fmt.Errorf("marshaling hook context: %w", err)
	}

	if err := readWriter.MkdirAll(HookContextDir, 0755); err != nil {
		return nil, fmt.Errorf("creating hook context directory: %w", err)
	}

	if err := readWriter.WriteFile(HookContextPath, jsonBytes, HookContextMode); err != nil {
		return nil, fmt.Errorf("writing hook context file: %w", err)
	}

	return jsonBytes, nil
}
