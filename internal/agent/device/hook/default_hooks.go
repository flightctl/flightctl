package hook

import (
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
)

func marshalExecutable(run string, envVars *[]string, workDir string, timeout string) v1alpha1.HookAction {
	var hookAction v1alpha1.HookAction
	executable := v1alpha1.HookActionExecutableSpec{}
	executable.Executable.EnvVars = envVars
	executable.Executable.Run = run
	executable.Executable.WorkDir = &workDir
	executable.Executable.Timeout = &timeout
	_ = hookAction.FromHookActionExecutableSpec(executable)
	return hookAction
}

func executableActionFactory(run string, envVars *[]string, workDir string, timeout string) ActionHookFactory {
	return newApiHookActionFactory(marshalExecutable(run, envVars, workDir, timeout))
}

func defaultAfterUpdateHooks() []HookDefinition {
	return []HookDefinition{
		{
			name:        "podman compose up",
			path:        "/var/run/flightctl/compose",
			description: "Bring up a multi-container system based on the provided YAML podman-compose definition file",
			ops:         []v1alpha1.FileOperation{v1alpha1.FileOperationCreate, v1alpha1.FileOperationUpdate, v1alpha1.FileOperationReboot},
			actionHooks: []ActionHookFactory{
				builtinHookFactory(client.PodmanComposeUp),
			},
		},
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
			name:        "podman compose down",
			path:        "/var/run/flightctl/compose",
			description: "Bring down a multi-container system based on the provided YAML podman-compose definition file",
			ops:         []v1alpha1.FileOperation{v1alpha1.FileOperationUpdate, v1alpha1.FileOperationRemove},
			actionHooks: []ActionHookFactory{
				builtinHookFactory(client.PodmanComposeDown),
			},
		},
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
