package firmware_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/stretchr/testify/require"
)

// agentConfigYAML is a minimal loadable agent configuration.
const agentConfigYAML = `enrollment-service:
  service:
    server: https://enrollment.endpoint
    certificate-authority-data: abcd
  authentication:
    client-certificate-data: efgh
    client-key-data: ijkl
management-service:
  service:
    server: https://management.endpoint
    certificate-authority-data: abcd
  authentication:
    client-certificate-data: efgh
    client-key-data: ijkl
`

// exampleMaxima is the documented local updater-access example.
var exampleMaxima = config.UpdaterAccessPolicy{
	Prepare: config.UpdaterAccessGrant{
		NetworkRepositories:    []string{"vendor-firmware"},
		CredentialRepositories: []string{"vendor-firmware"},
		ReadPaths:              []string{"/etc/vendor"},
		ReadDevices:            []string{"/dev/mtd0"},
		WorkspaceWrite:         true,
	},
	Apply: config.UpdaterAccessGrant{
		ReadPaths:      []string{"/etc/vendor"},
		WritePaths:     []string{"/opt/vendor"},
		ReadDevices:    []string{"/dev/mtd0"},
		WriteDevices:   []string{"/dev/mtd0"},
		WorkspaceWrite: true,
	},
	Verify: config.UpdaterAccessGrant{
		ReadPaths:   []string{"/etc/vendor", "/opt/vendor"},
		ReadDevices: []string{"/dev/mtd0"},
	},
}

func emptyRequest() *policy.PhaseAccess {
	return &policy.PhaseAccess{Prepare: &policy.AccessGrant{}, Apply: &policy.AccessGrant{}, Verify: &policy.AccessGrant{}}
}

func request(phase string, grant policy.AccessGrant) *policy.PhaseAccess {
	r := emptyRequest()
	switch phase {
	case "prepare":
		r.Prepare = &grant
	case "apply":
		r.Apply = &grant
	case "verify":
		r.Verify = &grant
	}
	return r
}

func setField(g *policy.AccessGrant, field, value string) {
	switch field {
	case "network-repositories":
		g.NetworkRepositories = []string{value}
	case "credential-repositories":
		g.CredentialRepositories = []string{value}
	case "read-paths":
		g.ReadPaths = []string{value}
	case "write-paths":
		g.WritePaths = []string{value}
	case "read-devices":
		g.ReadDevices = []string{value}
	case "write-devices":
		g.WriteDevices = []string{value}
	case "workspace-write":
		g.WorkspaceWrite = true
	}
}

func maximaFor(phase string) config.UpdaterAccessGrant {
	switch phase {
	case "prepare":
		return exampleMaxima.Prepare
	case "apply":
		return exampleMaxima.Apply
	default:
		return exampleMaxima.Verify
	}
}

func allowedValue(g config.UpdaterAccessGrant, field string) (string, bool) {
	var values []string
	switch field {
	case "network-repositories":
		values = g.NetworkRepositories
	case "credential-repositories":
		values = g.CredentialRepositories
	case "read-paths":
		values = g.ReadPaths
	case "write-paths":
		values = g.WritePaths
	case "read-devices":
		values = g.ReadDevices
	case "write-devices":
		values = g.WriteDevices
	case "workspace-write":
		return "true", g.WorkspaceWrite
	}
	if len(values) == 0 {
		return "", false
	}
	return values[0], true
}

func outsideValue(field string) string {
	switch field {
	case "network-repositories", "credential-repositories":
		return "other-repository"
	case "read-paths", "write-paths":
		return "/srv/other"
	default:
		return "/dev/other"
	}
}

func TestCheckAccess(t *testing.T) {
	testCases := []struct {
		name      string
		requested *policy.PhaseAccess
		maxima    config.UpdaterAccessPolicy
		permit    bool
	}{
		{"When the requested write path equals the maximum it should permit", request("apply", policy.AccessGrant{WritePaths: []string{"/opt/vendor"}}), exampleMaxima, true},
		{"When the requested write path lies beneath the maximum it should permit", request("apply", policy.AccessGrant{WritePaths: []string{"/opt/vendor/fw/bin"}}), exampleMaxima, true},
		{"When the requested write path only shares a string prefix it should deny", request("apply", policy.AccessGrant{WritePaths: []string{"/opt/vendor-other"}}), exampleMaxima, false},
		{"When the requested write path is an ancestor of the maximum it should deny", request("apply", policy.AccessGrant{WritePaths: []string{"/opt"}}), exampleMaxima, false},
		{"When a write is requested where only a read is allowed it should deny", request("prepare", policy.AccessGrant{WritePaths: []string{"/etc/vendor"}}), exampleMaxima, false},
		{"When nothing is requested and nothing is allowed it should permit", emptyRequest(), config.UpdaterAccessPolicy{}, true},
		{"When a different repository is requested it should deny", request("prepare", policy.AccessGrant{NetworkRepositories: []string{"vendor-firmware2"}}), exampleMaxima, false},
		{"When a repository differs only by case it should deny", request("prepare", policy.AccessGrant{NetworkRepositories: []string{"Vendor-Firmware"}}), exampleMaxima, false},
		{"When a device beneath an allowed device prefix is requested it should deny", request("apply", policy.AccessGrant{WriteDevices: []string{"/dev/mtd0"}}),
			config.UpdaterAccessPolicy{Apply: config.UpdaterAccessGrant{WriteDevices: []string{"/dev/mtd"}}}, false},
		{"When the requested device equals the allowed device it should permit", request("apply", policy.AccessGrant{WriteDevices: []string{"/dev/mtd0"}}), exampleMaxima, true},
		{"When workspace writes are requested but not allowed it should deny", request("prepare", policy.AccessGrant{WorkspaceWrite: true}),
			config.UpdaterAccessPolicy{Prepare: config.UpdaterAccessGrant{WorkspaceWrite: false}}, false},
		{"When the requested access is missing it should deny", nil, exampleMaxima, false},
		{"When a requested phase is missing it should deny", &policy.PhaseAccess{Prepare: &policy.AccessGrant{}, Apply: &policy.AccessGrant{}}, exampleMaxima, false},
	}

	for _, phase := range []string{"prepare", "apply", "verify"} {
		for _, field := range []string{"network-repositories", "credential-repositories", "read-paths", "write-paths", "read-devices", "write-devices", "workspace-write"} {
			within := policy.AccessGrant{}
			if value, ok := allowedValue(maximaFor(phase), field); ok {
				setField(&within, field, value)
			}
			testCases = append(testCases, struct {
				name      string
				requested *policy.PhaseAccess
				maxima    config.UpdaterAccessPolicy
				permit    bool
			}{fmt.Sprintf("When %s %s stays within the maxima it should permit", phase, field), request(phase, within), exampleMaxima, true})

			outside := policy.AccessGrant{}
			if _, allowed := allowedValue(maximaFor(phase), field); field == "workspace-write" && allowed {
				continue
			}
			setField(&outside, field, outsideValue(field))
			testCases = append(testCases, struct {
				name      string
				requested *policy.PhaseAccess
				maxima    config.UpdaterAccessPolicy
				permit    bool
			}{fmt.Sprintf("When %s %s exceeds the maxima it should deny", phase, field), request(phase, outside), exampleMaxima, false})
		}
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := firmware.CheckAccess(tc.requested, tc.maxima)
			if tc.permit {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestCheckAccessDeniesUnderInvalidLocalConfiguration(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	t.Setenv(config.TestRootDirEnvKey, root)
	configDir := filepath.Join(root, config.DefaultConfigDir)
	require.NoError(os.MkdirAll(configDir, 0o755))
	invalid := agentConfigYAML + "firmware:\n  updater-access:\n    verify:\n      read-paths: [/etc/vendor]\n      write-paths: [/opt/vendor]\n"
	require.NoError(os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(invalid), 0o600))
	cfg, err := config.Load(config.DefaultConfigFile)
	require.NoError(err)
	require.NotEmpty(cfg.Warnings)

	require.Error(firmware.CheckAccess(request("verify", policy.AccessGrant{ReadPaths: []string{"/etc/vendor"}}), cfg.Firmware.UpdaterAccess))
	require.NoError(firmware.CheckAccess(emptyRequest(), cfg.Firmware.UpdaterAccess))
}

func TestCheckDependencies(t *testing.T) {
	testCases := []struct {
		name   string
		deps   []policy.RuntimeDependency
		permit bool
	}{
		{"When no dependencies are declared it should permit", nil, true},
		{"When an empty dependency list is declared it should permit", []policy.RuntimeDependency{}, true},
		{"When one dependency is declared it should deny", []policy.RuntimeDependency{{Name: "fwupd", Constraint: ">=1.9"}}, false},
		{"When several dependencies are declared it should deny", []policy.RuntimeDependency{{Name: "fwupd", Constraint: ">=1.9"}, {Name: "efivar", Constraint: "any"}}, false},
		{"When a dependency has a free-form constraint it should deny", []policy.RuntimeDependency{{Name: "openssl", Constraint: "3.x on the host"}}, false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := firmware.CheckDependencies(tc.deps)
			if tc.permit {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
