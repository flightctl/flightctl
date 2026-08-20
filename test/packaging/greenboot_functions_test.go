package packaging_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("cannot find repo root: %v", err)
	}
	return strings.TrimSpace(string(root))
}

func runBash(t *testing.T, script string) string {
	t.Helper()
	out, err := exec.Command("bash", "-c", script).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("bash failed: %v\nstderr: %s", err, exitErr.Stderr)
		}
		t.Fatalf("bash failed: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func runFindBlockedVendorHealthchecks(t *testing.T, usrLibDir, etcDir string) string {
	t.Helper()
	root := repoRoot(t)
	functionsPath := filepath.Join(root, "packaging", "greenboot", "functions.sh")

	return runBash(t,
		"source '"+functionsPath+"' && "+
			"GREENBOOT_USR_LIB_REQUIRED_D='"+usrLibDir+"' "+
			"GREENBOOT_ETC_REQUIRED_D='"+etcDir+"' "+
			"find_blocked_vendor_healthchecks")
}

func runSetDisabledHealthchecks(t *testing.T, confPath, disabledScripts string) {
	t.Helper()
	root := repoRoot(t)
	functionsPath := filepath.Join(root, "packaging", "greenboot", "functions.sh")

	runBash(t, "source '"+functionsPath+"' && GREENBOOT_CONF='"+confPath+"' set_disabled_healthchecks '"+disabledScripts+"'")
}

func writeScript(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/bash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindBlockedVendorHealthchecks(t *testing.T) {
	t.Run("When MicroShift script is in usr lib it should be disabled", func(t *testing.T) {
		usrLib := t.TempDir()
		etc := t.TempDir()
		writeScript(t, usrLib, "40_microshift_running_check.sh")
		got := runFindBlockedVendorHealthchecks(t, usrLib, etc)
		if !strings.Contains(got, "40_microshift_running_check.sh") {
			t.Fatalf("expected MicroShift script in disabled list, got %q", got)
		}
	})

	t.Run("When MicroShift script is in etc it should be disabled", func(t *testing.T) {
		usrLib := t.TempDir()
		etc := t.TempDir()
		writeScript(t, etc, "40_microshift_running_check.sh")
		got := runFindBlockedVendorHealthchecks(t, usrLib, etc)
		if !strings.Contains(got, "40_microshift_running_check.sh") {
			t.Fatalf("expected MicroShift script in disabled list, got %q", got)
		}
	})

	t.Run("When MicroShift is in both paths it should appear once", func(t *testing.T) {
		usrLib := t.TempDir()
		etc := t.TempDir()
		writeScript(t, usrLib, "40_microshift_running_check.sh")
		writeScript(t, etc, "40_microshift_running_check.sh")
		got := runFindBlockedVendorHealthchecks(t, usrLib, etc)
		if strings.Count(got, "40_microshift_running_check.sh") != 1 {
			t.Fatalf("expected MicroShift listed once, got %q", got)
		}
	})

	t.Run("When customer script is in usr lib it should not be disabled", func(t *testing.T) {
		usrLib := t.TempDir()
		etc := t.TempDir()
		writeScript(t, usrLib, "50_customer_selftest.sh")
		got := runFindBlockedVendorHealthchecks(t, usrLib, etc)
		if got != "" {
			t.Fatalf("expected no disabled scripts, got %q", got)
		}
	})

	t.Run("When customer script is in etc it should not be disabled", func(t *testing.T) {
		usrLib := t.TempDir()
		etc := t.TempDir()
		writeScript(t, etc, "50_customer_selftest.sh")
		got := runFindBlockedVendorHealthchecks(t, usrLib, etc)
		if got != "" {
			t.Fatalf("expected no disabled scripts, got %q", got)
		}
	})

	t.Run("When only flightctl and core scripts are present it should not disable them", func(t *testing.T) {
		usrLib := t.TempDir()
		etc := t.TempDir()
		for _, name := range []string{
			"20_check_flightctl_agent.sh",
			"00_required_scripts_start.sh",
			"01_repository_dns_check.sh",
			"02_watchdog.sh",
		} {
			writeScript(t, usrLib, name)
		}
		got := runFindBlockedVendorHealthchecks(t, usrLib, etc)
		if got != "" {
			t.Fatalf("expected no disabled scripts, got %q", got)
		}
	})

	t.Run("When MicroShift is in etc and customer is in etc it should disable only MicroShift", func(t *testing.T) {
		usrLib := t.TempDir()
		etc := t.TempDir()
		writeScript(t, etc, "40_microshift_running_check.sh")
		writeScript(t, etc, "50_customer_selftest.sh")
		got := runFindBlockedVendorHealthchecks(t, usrLib, etc)
		if !strings.Contains(got, "40_microshift_running_check.sh") {
			t.Fatalf("expected MicroShift script in disabled list, got %q", got)
		}
		if strings.Contains(got, "50_customer_selftest.sh") {
			t.Fatalf("customer script must not be disabled, got %q", got)
		}
	})

	t.Run("When MicroShift is in usr lib and customer is in etc it should disable only MicroShift", func(t *testing.T) {
		usrLib := t.TempDir()
		etc := t.TempDir()
		writeScript(t, usrLib, "40_microshift_running_check.sh")
		writeScript(t, etc, "50_customer_selftest.sh")
		got := runFindBlockedVendorHealthchecks(t, usrLib, etc)
		if !strings.Contains(got, "40_microshift_running_check.sh") {
			t.Fatalf("expected MicroShift script in disabled list, got %q", got)
		}
		if strings.Contains(got, "50_customer_selftest.sh") {
			t.Fatalf("customer script must not be disabled, got %q", got)
		}
	})
}

func TestSetDisabledHealthchecks(t *testing.T) {
	t.Run("When stale customer scripts were disabled it should clear them on empty update", func(t *testing.T) {
		confPath := filepath.Join(t.TempDir(), "greenboot.conf")
		stale := "DISABLED_HEALTHCHECKS=(\"50_customer_selftest.sh\" \"40_microshift_running_check.sh\")\n"
		if err := os.WriteFile(confPath, []byte(stale), 0o644); err != nil {
			t.Fatal(err)
		}

		runSetDisabledHealthchecks(t, confPath, "")

		content, err := os.ReadFile(confPath)
		if err != nil {
			t.Fatal(err)
		}
		got := string(content)
		if strings.Contains(got, "50_customer_selftest.sh") {
			t.Fatalf("stale customer script must be cleared, got %q", got)
		}
		if !strings.Contains(got, "DISABLED_HEALTHCHECKS=()") {
			t.Fatalf("expected empty DISABLED_HEALTHCHECKS, got %q", got)
		}
	})

	t.Run("When MicroShift is present it should set only blocklisted scripts", func(t *testing.T) {
		confPath := filepath.Join(t.TempDir(), "greenboot.conf")
		if err := os.WriteFile(confPath, []byte("DISABLED_HEALTHCHECKS=(\"50_customer_selftest.sh\")\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		runSetDisabledHealthchecks(t, confPath, " \"40_microshift_running_check.sh\"")

		content, err := os.ReadFile(confPath)
		if err != nil {
			t.Fatal(err)
		}
		got := string(content)
		if strings.Contains(got, "50_customer_selftest.sh") {
			t.Fatalf("stale customer script must be cleared, got %q", got)
		}
		if !strings.Contains(got, "40_microshift_running_check.sh") {
			t.Fatalf("expected MicroShift in DISABLED_HEALTHCHECKS, got %q", got)
		}
	})
}

func TestFindBlockedVendorHealthchecks_Regression(t *testing.T) {
	t.Run("When customer script is present blanket old logic would have disabled it", func(t *testing.T) {
		usrLib := t.TempDir()
		etc := t.TempDir()
		writeScript(t, usrLib, "50_customer_selftest.sh")

		got := runFindBlockedVendorHealthchecks(t, usrLib, etc)
		if got != "" {
			t.Fatalf("fix must leave customer script enabled (empty disabled list), got %q", got)
		}

		// Simulate prior blanket disable: any non-allowlisted script was disabled.
		oldWouldDisable := runBash(t, `
			dir='`+usrLib+`'
			scripts=""
			for script in "$dir"/*.sh; do
				[ -f "$script" ] || continue
				name=$(basename "$script")
				case "$name" in
					*flightctl*) continue ;;
					00_required_scripts_start.sh) continue ;;
					01_repository_dns_check.sh) continue ;;
					02_watchdog.sh) continue ;;
				esac
				scripts="$scripts \"$name\""
			done
			echo "$scripts"
		`)
		if !strings.Contains(oldWouldDisable, "50_customer_selftest.sh") {
			t.Fatalf("old logic simulation should have disabled customer script, got %q", oldWouldDisable)
		}
	})
}
