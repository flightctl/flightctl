package firmware

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/flightctl/flightctl/internal/agent/config"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
)

// CheckDependencies fails for any declared runtime dependency: only
// dependency-free updater releases are supported.
func CheckDependencies(deps []policy.RuntimeDependency) error {
	if len(deps) == 0 {
		return nil
	}
	names := make([]string, 0, len(deps))
	for _, d := range deps {
		names = append(names, d.Name)
	}
	return fmt.Errorf("updater release declares runtime dependencies (%s); only dependency-free updater releases are supported", strings.Join(names, ", "))
}

// CheckAccess reports whether every requested grant lies within the local
// policy. Repositories and devices must match exactly, a path must equal or lie
// beneath an allowed path of the same kind, and workspace writes must be
// allowed explicitly. Passing this check does not confine the updater at
// runtime.
func CheckAccess(requested *policy.PhaseAccess, allowed config.UpdaterAccessPolicy) error {
	if requested == nil || requested.Prepare == nil || requested.Apply == nil || requested.Verify == nil {
		return errors.New("requested updater access must name every phase")
	}
	for _, phase := range []struct {
		name      string
		requested *policy.AccessGrant
		allowed   config.UpdaterAccessGrant
	}{
		{"prepare", requested.Prepare, allowed.Prepare},
		{"apply", requested.Apply, allowed.Apply},
		{"verify", requested.Verify, allowed.Verify},
	} {
		if denied := deniedGrant(phase.requested, phase.allowed); denied != "" {
			return fmt.Errorf("updater requests %s %s outside the local firmware.updater-access policy", phase.name, denied)
		}
	}
	return nil
}

func deniedGrant(r *policy.AccessGrant, a config.UpdaterAccessGrant) string {
	for _, f := range []struct {
		name      string
		requested []string
		allowed   []string
		permits   func(requested, allowed string) bool
	}{
		{"network repository", r.NetworkRepositories, a.NetworkRepositories, sameEntry},
		{"credential repository", r.CredentialRepositories, a.CredentialRepositories, sameEntry},
		{"read path", r.ReadPaths, a.ReadPaths, withinPath},
		{"write path", r.WritePaths, a.WritePaths, withinPath},
		{"read device", r.ReadDevices, a.ReadDevices, sameEntry},
		{"write device", r.WriteDevices, a.WriteDevices, sameEntry},
	} {
		for _, value := range f.requested {
			if !slices.ContainsFunc(f.allowed, func(allowed string) bool { return f.permits(value, allowed) }) {
				return fmt.Sprintf("%s %q", f.name, value)
			}
		}
	}
	if r.WorkspaceWrite && !a.WorkspaceWrite {
		return "workspace write"
	}
	return ""
}

func sameEntry(requested, allowed string) bool { return requested == allowed }

// withinPath compares path components, so /opt/vendor permits /opt/vendor/fw
// but not /opt/vendor-other.
func withinPath(requested, allowed string) bool {
	return path.Clean(requested) == requested && (requested == allowed || strings.HasPrefix(requested, allowed+"/"))
}
