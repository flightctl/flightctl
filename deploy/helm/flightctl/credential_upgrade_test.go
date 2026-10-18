package flightctl_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

func TestCredentialWriterUpgradeShutdown(t *testing.T) {
	for _, condition := range []string{"always", "chart", "never"} {
		t.Run("When optional shutdown is "+condition+" it should still stop credential writers in their namespaces", func(t *testing.T) {
			_, filename, _, ok := runtime.Caller(0)
			require.True(t, ok)
			output, err := exec.Command("helm", "template", "flightctl", filepath.Dir(filename), "--namespace", "public",
				"--is-upgrade", "--set", "global.internalNamespace=private", "--set", "global.auth.type=none",
				"--set", "global.generateCertificates=builtin", "--set", "upgradeHooks.scaleDown.condition="+condition,
				"--set-json", "upgradeHooks.scaleDown.deployments=[]", "--show-only", "templates/flightctl-preupgrade-scale-jobs.yaml").CombinedOutput()
			require.NoError(t, err, string(output))
			var job batchv1.Job
			roles := make(map[string]rbacv1.Role)
			bindings := make(map[string]rbacv1.RoleBinding)
			for _, document := range strings.Split(string(output), "\n---") {
				var metadata struct{ Kind string }
				require.NoError(t, yaml.Unmarshal([]byte(document), &metadata))
				switch metadata.Kind {
				case "Job":
					require.NoError(t, yaml.Unmarshal([]byte(document), &job))
				case "Role":
					var role rbacv1.Role
					require.NoError(t, yaml.Unmarshal([]byte(document), &role))
					roles[role.Namespace] = role
				case "RoleBinding":
					var binding rbacv1.RoleBinding
					require.NoError(t, yaml.Unmarshal([]byte(document), &binding))
					bindings[binding.Namespace] = binding
				}
			}
			require.Equal(t, "private", job.Namespace)
			require.Len(t, job.Spec.Template.Spec.Containers, 1)
			script := strings.Join(job.Spec.Template.Spec.Containers[0].Args, "\n")
			require.Contains(t, script, "public/flightctl-api")
			require.Contains(t, script, "private/flightctl-imagebuilder-worker")
			require.Contains(t, script, "--for=delete pod")
			require.NotContains(t, script, "|| true")
			for _, namespace := range []string{"public", "private"} {
				require.Contains(t, roles, namespace)
				require.Contains(t, bindings, namespace)
				require.Equal(t, "private", bindings[namespace].Subjects[0].Namespace)
				require.Equal(t, rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "flightctl-upgrade"}, bindings[namespace].RoleRef)
				require.Contains(t, roles[namespace].Rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}})
			}
		})
	}
}
