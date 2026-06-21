package applications

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	// vmConsecutiveFailureThreshold is the number of consecutive virsh domstate
	// failures before reporting ApplicationStatusError. At a 30 s sync cadence,
	// 3 failures provide ~90 s of grace for VM startup.
	vmConsecutiveFailureThreshold = 3

	// vmStatusPollTimeout is the per-poll deadline for the podman exec call.
	vmStatusPollTimeout = 5 * time.Second

	// vmStatusCacheTTL is how long a successful virsh domstate result is reused
	// before the next exec call. This reduces podman exec frequency (and the
	// resulting journal noise) while keeping state changes detectable within one TTL.
	vmStatusCacheTTL = 30 * time.Second

	// virtLauncherContainerSuffix is appended to the app name to form the
	// container name used by KubeVirt's virt-launcher pod.
	virtLauncherContainerSuffix = "-compute"
	virtLauncherContainerPrefix = "virt-launcher-"

	// virtLauncherDomainNamespace is the Kubernetes namespace prefix that
	// KubeVirt's virt-launcher uses when naming the libvirt domain:
	// "{namespace}_{vmName}". In standalone (non-Kubernetes) deployments the
	// namespace is always "default".
	virtLauncherDomainNamespace = "default"
)

// vmDomainState is the raw domain state string returned by virsh domstate.
type vmDomainState string

const (
	vmDomainStateRunning    vmDomainState = "running"
	vmDomainStateShutOff    vmDomainState = "shut off"
	vmDomainStateInShutdown vmDomainState = "in shutdown"
	vmDomainStatePaused     vmDomainState = "paused"
	vmDomainStateCrashed    vmDomainState = "crashed"
)

// vmStatusPoller polls the libvirt domain state for a VM workload by executing
// virsh domstate inside the virt-launcher container via podman exec.
// Results are cached for vmStatusCacheTTL to reduce exec frequency.
type vmStatusPoller struct {
	exec                executer.Executer
	log                 *log.PrefixLogger
	appName             string
	consecutiveFailures int
	maxFailures         int
	cachedStatus        v1beta1.ApplicationStatusType
	cacheExpiry         time.Time
}

// newVMStatusPoller returns a vmStatusPoller for the given application name.
func newVMStatusPoller(exec executer.Executer, log *log.PrefixLogger, appName string) *vmStatusPoller {
	return &vmStatusPoller{
		exec:        exec,
		log:         log,
		appName:     appName,
		maxFailures: vmConsecutiveFailureThreshold,
	}
}

// Poll returns the cached domain status if the cache is still valid, otherwise
// executes virsh domstate inside the virt-launcher container and refreshes the cache.
// A non-zero exit code is treated as a transient failure: the consecutive failure
// counter is incremented and ApplicationStatusStarting is returned until maxFailures
// is reached, at which point ApplicationStatusError is returned. On a successful
// exit the counter is reset, the cache is updated, and the domain state is mapped.
func (p *vmStatusPoller) Poll(ctx context.Context) v1beta1.ApplicationStatusType {
	if time.Now().Before(p.cacheExpiry) {
		return p.cachedStatus
	}

	container := fmt.Sprintf("%s%s%s", virtLauncherContainerPrefix, p.appName, virtLauncherContainerSuffix)
	domain := fmt.Sprintf("%s_%s", virtLauncherDomainNamespace, p.appName)
	stdout, stderr, exitCode := p.exec.ExecuteWithContext(ctx, "podman", "exec", container, "virsh", "domstate", domain)
	if exitCode != 0 {
		p.consecutiveFailures++
		p.log.Debugf("virsh domstate for %q exited %d (failure %d/%d): %s", p.appName, exitCode, p.consecutiveFailures, p.maxFailures, strings.TrimSpace(stderr))
		if p.consecutiveFailures >= p.maxFailures {
			return v1beta1.ApplicationStatusError
		}
		return v1beta1.ApplicationStatusStarting
	}

	p.consecutiveFailures = 0
	status := mapDomainState(vmDomainState(strings.TrimSpace(stdout)))
	p.cachedStatus = status
	p.cacheExpiry = time.Now().Add(vmStatusCacheTTL)
	return status
}

// mapDomainState maps a virsh domstate output string to an ApplicationStatusType.
// Unknown output strings map to ApplicationStatusError.
func mapDomainState(state vmDomainState) v1beta1.ApplicationStatusType {
	switch state {
	case vmDomainStateRunning:
		return v1beta1.ApplicationStatusRunning
	case vmDomainStateShutOff:
		return v1beta1.ApplicationStatusStopped
	case vmDomainStateInShutdown:
		return v1beta1.ApplicationStatusStopping
	default:
		return v1beta1.ApplicationStatusError
	}
}
