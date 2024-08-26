package hook

import (
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
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

func defaultAfterUpdateHooks() []v1alpha1.DeviceUpdateHookSpec {
	return []v1alpha1.DeviceUpdateHookSpec{
		{
			Name:        util.StrToPtr("podman compose up"),
			Path:        util.StrToPtr("/var/run/flightctl/compose"),
			Description: util.StrToPtr("Bring up a multi-container system based on the provided YAML podman-compose definition file"),
			OnFile:      &[]v1alpha1.FileOperation{v1alpha1.FileOperationCreate, v1alpha1.FileOperationUpdate, v1alpha1.FileOperationReboot},
			Actions: []v1alpha1.HookAction{
				marshalExecutable("podman-compose -f {{ .FilePath }} up -d", nil,
					"/var/run/flightctl/compose", "1m"),
			},
		},
	}
}

func defaultBeforeUpdateHooks() []v1alpha1.DeviceUpdateHookSpec {
	return []v1alpha1.DeviceUpdateHookSpec{
		{
			Name:        util.StrToPtr("podman compose down"),
			Path:        util.StrToPtr("/var/run/flightctl/compose"),
			Description: util.StrToPtr("Bring down a multi-container system based on the provided YAML podman-compose definition file"),
			OnFile:      &[]v1alpha1.FileOperation{v1alpha1.FileOperationUpdate, v1alpha1.FileOperationRemove},
			Actions: []v1alpha1.HookAction{
				marshalExecutable("podman-compose -f {{ .FilePath }} down", nil,
					"/var/run/flightctl/compose", "1m"),
			},
		},
	}
}
