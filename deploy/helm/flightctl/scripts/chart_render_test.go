package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestChartRenderOfflineSafe(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not available")
	}

	chartDir := chartDirPath(t)
	lintValues := filepath.Join(chartDir, "lint-values.yaml")

	t.Run("When rendering offline it should emit migration hooks and password ensure job", func(t *testing.T) {
		out := helmTemplate(t, chartDir, lintValues)

		if !strings.Contains(out, "name: flightctl-db-migration-1\n") {
			t.Error("expected migration job")
		}
		if !strings.Contains(out, "helm.sh/hook: pre-install,pre-upgrade") {
			t.Error("expected pre-install,pre-upgrade hooks")
		}
		if !strings.Contains(out, "name: flightctl-password-secrets-1\n") &&
			!strings.Contains(out, "name: flightctl-password-secrets-1\r\n") {
			// helm may quote differently; accept either form
			if !strings.Contains(out, "flightctl-password-secrets-1") {
				t.Error("expected password-secrets ensure job")
			}
		}
		if !strings.Contains(out, "ensure-password-secrets.sh") {
			t.Error("expected ensure-password-secrets script in rendered output")
		}
		if !strings.Contains(out, "helm.sh/resource-policy=keep") {
			t.Error("expected keep annotation in ensure script")
		}
	})

	t.Run("When rendering offline it should not emit chart-managed password Secret resources", func(t *testing.T) {
		out := helmTemplate(t, chartDir, lintValues)
		for _, name := range []string{
			"flightctl-db-admin-secret",
			"flightctl-db-app-secret",
			"flightctl-db-migration-secret",
			"flightctl-kv-secret",
		} {
			if secretResourceRendered(out, name) {
				t.Errorf("password Secret %s should not be rendered as a chart-managed resource", name)
			}
		}
	})

	t.Run("When scaleDown condition is chart and lookup is empty it should omit scale-down job", func(t *testing.T) {
		out := helmTemplate(t, chartDir, lintValues)
		if strings.Contains(out, "flightctl-preupgrade-scale-to-zero") {
			t.Error("scale-down job should be omitted when Deployments are not visible")
		}
	})

	t.Run("When scaleDown condition is always it should emit scale-down job hooks", func(t *testing.T) {
		out := helmTemplate(t, chartDir, lintValues, "--set", "upgradeHooks.scaleDown.condition=always")
		if !strings.Contains(out, "flightctl-preupgrade-scale-to-zero") {
			t.Fatal("expected scale-down job")
		}
		idx := strings.Index(out, "name: flightctl-preupgrade-scale-to-zero")
		window := out[idx:min(len(out), idx+400)]
		if !strings.Contains(window, "pre-install,pre-upgrade") {
			t.Errorf("expected pre-install,pre-upgrade on scale-down job, got:\n%s", window)
		}
	})

	t.Run("When BYO secret names are set it should omit password ensure job", func(t *testing.T) {
		out := helmTemplate(t, chartDir, lintValues,
			"--set", "kv.passwordSecretName=my-kv",
			"--set", "db.builtin.masterUserSecretName=my-admin",
			"--set", "db.builtin.applicationUserSecretName=my-app",
			"--set", "db.builtin.migrationUserSecretName=my-mig",
		)
		if strings.Contains(out, "flightctl-password-secrets-") {
			t.Error("password ensure job should be omitted for BYO secrets")
		}
	})

	t.Run("When image is not OS-qualified it should rewrite on render", func(t *testing.T) {
		out := helmTemplate(t, chartDir, lintValues,
			"--set", "api.image.image=quay.io/flightctl/flightctl-api",
		)
		if !strings.Contains(out, "quay.io/flightctl/flightctl-api-el9") {
			t.Error("expected OS-qualified image rewrite")
		}
	})
}

func chartDirPath(t *testing.T) string {
	t.Helper()
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(filename), ".."))
}

func helmTemplate(t *testing.T, chartDir, valuesFile string, extraArgs ...string) string {
	t.Helper()
	args := []string{
		"template", "test-release", chartDir,
		"--namespace", "flightctl",
		"-f", valuesFile,
	}
	args = append(args, extraArgs...)
	cmd := exec.Command("helm", args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	return string(out)
}

// secretResourceRendered reports whether a Secret resource with the given name
// appears as a top-level chart object (not merely mentioned in a Job script).
func secretResourceRendered(rendered, name string) bool {
	parts := strings.Split(rendered, "\n---\n")
	for _, part := range parts {
		if !strings.Contains(part, "kind: Secret") {
			continue
		}
		if strings.Contains(part, "name: "+name+"\n") || strings.Contains(part, "name: "+name+"\r\n") {
			return true
		}
	}
	return false
}
