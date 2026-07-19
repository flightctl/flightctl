package packaging_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const containerImage = "quay.io/centos/centos:stream9"

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("podman"); err != nil {
		fmt.Fprintln(os.Stderr, "podman not found, skipping packaging tests")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func rpmDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("RPM_DIR")
	if dir == "" {
		root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			t.Fatalf("cannot find repo root: %v", err)
		}
		dir = filepath.Join(strings.TrimSpace(string(root)), "bin", "rpm")
	}
	for _, pattern := range []string{"flightctl-agent-*.rpm", "flightctl-selinux-*.rpm", "flightctl-greenboot-*.rpm"} {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		if len(matches) == 0 {
			t.Skipf("required RPM %s not found in %s (run 'make rpm' first)", pattern, dir)
		}
	}
	return dir
}

type container struct {
	name string
	t    *testing.T
}

func startContainer(t *testing.T, name string, rpmDir string) *container {
	t.Helper()
	c := &container{name: name, t: t}

	run(t, "podman", "rm", "-f", name)

	mustRun(t, "podman", "run", "-d", "--name", name,
		"-v", rpmDir+":/rpms-src:Z,ro",
		containerImage, "sleep", "infinity")

	mustExec(t, name, "bash", "-c", `
		set -e
		mkdir -p /rpms
		cp /rpms-src/*.rpm /rpms/ 2>/dev/null || true
		dnf install -y -q createrepo_c 2>/dev/null
		createrepo_c /rpms >/dev/null 2>&1
		cat > /etc/yum.repos.d/flightctl-local.repo <<EOF
[flightctl-local]
name=flightctl local RPMs
baseurl=file:///rpms
enabled=1
gpgcheck=0
EOF
	`)

	t.Cleanup(func() { run(t, "podman", "rm", "-f", name) })
	return c
}

func (c *container) exec(args ...string) (string, error) {
	cmdArgs := append([]string{"exec", c.name}, args...)
	out, err := exec.Command("podman", cmdArgs...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (c *container) mustExec(args ...string) string {
	c.t.Helper()
	out, err := c.exec(args...)
	if err != nil {
		c.t.Fatalf("podman exec %s %v failed: %v\n%s", c.name, args, err, out)
	}
	return out
}

func (c *container) assertInstalled(pkg string) {
	c.t.Helper()
	if _, err := c.exec("rpm", "-q", pkg); err != nil {
		c.t.Errorf("%s is NOT installed", pkg)
	}
}

func (c *container) assertNotInstalled(pkg string) {
	c.t.Helper()
	if _, err := c.exec("rpm", "-q", pkg); err == nil {
		c.t.Errorf("%s IS installed (should not be)", pkg)
	}
}

func (c *container) assertFileExists(path string) {
	c.t.Helper()
	if _, err := c.exec("test", "-f", path); err != nil {
		c.t.Errorf("%s does NOT exist", path)
	}
}

func (c *container) assertFileAbsent(path string) {
	c.t.Helper()
	if _, err := c.exec("test", "-f", path); err == nil {
		c.t.Errorf("%s exists (should not)", path)
	}
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	exec.Command(name, args...).Run() //nolint:errcheck
}

func mustRun(t *testing.T, name string, args ...string) {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}

func mustExec(t *testing.T, ctr string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"exec", ctr}, args...)
	out, err := exec.Command("podman", cmdArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("podman exec %s %v failed: %v\n%s", ctr, args, err, out)
	}
}

// AC-1: Package-mode install (install_weak_deps=False) succeeds without greenboot
func TestPackageModeInstall(t *testing.T) {
	dir := rpmDir(t)
	c := startContainer(t, "verify-rpm-pkg-mode", dir)

	c.mustExec("dnf", "install", "-y", "--setopt=install_weak_deps=False", "flightctl-agent")

	c.assertInstalled("flightctl-agent")
	c.assertInstalled("flightctl-selinux")
	c.assertNotInstalled("flightctl-greenboot")
	c.assertNotInstalled("greenboot")
	c.assertFileExists("/usr/bin/flightctl-agent")
	c.assertFileExists("/usr/lib/systemd/system/flightctl-agent.service")
	c.assertFileAbsent("/usr/libexec/flightctl/mask-bootc-timer.sh")
}

// AC-2: Image-mode install (default weak deps) pulls flightctl-greenboot
func TestImageModeInstall(t *testing.T) {
	dir := rpmDir(t)
	c := startContainer(t, "verify-rpm-img-mode", dir)

	c.mustExec("dnf", "install", "-y", "flightctl-agent")

	c.assertInstalled("flightctl-agent")
	c.assertInstalled("flightctl-greenboot")
	c.assertInstalled("greenboot")
	c.assertFileExists("/usr/libexec/flightctl/mask-bootc-timer.sh")
	c.assertFileExists("/usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh")
	c.assertFileExists("/usr/lib/systemd/system/flightctl-configure-greenboot.service")
}

// AC-3: Upgrade path — RPM metadata proves the Recommends + version-lock mechanism
func TestUpgradePathMechanism(t *testing.T) {
	dir := rpmDir(t)
	c := startContainer(t, "verify-rpm-upgrade", dir)

	c.mustExec("dnf", "install", "-y", "flightctl-agent")

	recommends := c.mustExec("rpm", "-q", "--recommends", "flightctl-agent")
	if !strings.Contains(recommends, "flightctl-greenboot") {
		t.Errorf("flightctl-agent does not Recommend flightctl-greenboot, got: %s", recommends)
	}

	requires := c.mustExec("rpm", "-q", "--requires", "flightctl-greenboot")
	found := false
	for _, line := range strings.Split(requires, "\n") {
		if strings.HasPrefix(line, "flightctl-agent") && strings.Contains(line, "=") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("flightctl-greenboot does not require flightctl-agent with version equality, got:\n%s", requires)
	}
}

// AC-4: Agent systemd unit start smoke test (package-mode, no greenboot)
func TestAgentUnitSmoke(t *testing.T) {
	dir := rpmDir(t)
	c := startContainer(t, "verify-rpm-unit-smoke", dir)

	c.mustExec("dnf", "install", "-y", "--setopt=install_weak_deps=False", "flightctl-agent")
	c.mustExec("dnf", "install", "-y", "-q", "systemd")

	unit := c.mustExec("cat", "/usr/lib/systemd/system/flightctl-agent.service")
	if !strings.Contains(unit, "test ! -x /usr/libexec/flightctl/mask-bootc-timer.sh") {
		t.Error("ExecStartPre does not use fail-closed wrapper for mask-bootc-timer.sh")
	}

	out, err := c.exec("systemd-analyze", "verify", "/usr/lib/systemd/system/flightctl-agent.service")
	if err != nil {
		if strings.Contains(strings.ToLower(out), "error") || strings.Contains(out, "Failed") {
			t.Errorf("systemd-analyze verify reported errors: %s", out)
		} else {
			t.Errorf("systemd-analyze verify failed unexpectedly: %v\nOutput: %s", err, out)
		}
	}
}
