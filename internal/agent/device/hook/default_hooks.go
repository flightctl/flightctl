package hook

import (
	"github.com/flightctl/flightctl/api/v1alpha1"
)

func marshalExecutable(run string, envVars *[]string, workDir string, timeout string) v1alpha1.HookAction {
	cha := v1alpha1.HookAction0{
		Executable: v1alpha1.HookActionExecutableSpec{
			EnvVars: envVars,
			Run:     run,
			WorkDir: &workDir,
			Timeout: &timeout,
		},
	}
	var ret v1alpha1.HookAction
	_ = ret.FromHookAction0(cha)
	return ret
}

func executableActionFactory(run string, envVars *[]string, workDir string, timeout string) ActionHookFactory {
	return newApiHookActionFactory(marshalExecutable(run, envVars, workDir, timeout))
}

func defaultAfterUpdateHooks() []HookDefinition {
	return []HookDefinition{
		{
			name:        "microshift manifest up",
			path:        "/var/usr/klusterlet-manifests",
			description: "Apply the provided microshift manifest",
			ops:         []v1alpha1.FileOperation{v1alpha1.FileOperationCreate, v1alpha1.FileOperationUpdate},
			actionHooks: []ActionHookFactory{
				executableActionFactory("kubectl apply -f {{ .FilePath }}", &[]string{"KUBECONFIG=/var/lib/microshift/resources/kubeadmin/kubeconfig"},
					"/var/usr/klusterlet-manifests", "1m"),
			},
		},
	}
}

func defaultBeforeUpdateHooks() []HookDefinition {
	return []HookDefinition{
		{
			name:        "microshift manifest down",
			path:        "/var/usr/klusterlet-manifests",
			description: "Delete the provided microshift manifest",
			ops:         []v1alpha1.FileOperation{v1alpha1.FileOperationRemove},
			actionHooks: []ActionHookFactory{
				executableActionFactory("kubectl delete -f {{ .FileName }}", &[]string{"KUBECONFIG=/var/lib/microshift/resources/kubeadmin/kubeconfig"},
					"/var/usr/klusterlet-manifests", "1m"),
			},
		},
	}
}
