package hook

import (
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/samber/lo"
)

func marshalExecutable(run string, envVars *[]string, workDir string, timeout string, on []v1alpha1.FileOperation) v1alpha1.HookAction {
	cha := v1alpha1.HookActionExecutableSpec{
		Executable: v1alpha1.HookActionExecutable{
			EnvVars: envVars,
			Run:     run,
			WorkDir: &workDir,
		},
		Timeout: &timeout,
		On:      &on,
	}
	var ret v1alpha1.HookAction
	_ = ret.FromHookActionExecutableSpec(cha)
	return ret
}

func defaultAfterUpdateHooks() []v1alpha1.DeviceHookSpec {
	return []v1alpha1.DeviceHookSpec{
		{
			Name:        lo.ToPtr("docker compose up"),
			Path:        lo.ToPtr("/var/run/flightctl/compose/docker"),
			Description: lo.ToPtr("Bring up a multi-container system based on the provided YAML docker-compose definition file"),
			Actions: []v1alpha1.HookAction{
				marshalExecutable("docker-compose -f {{ .FilePath }} up -d", lo.ToPtr([]string{"DOCKER_HOST=unix:///run/podman/podman.sock"}),
					"/var/run/flightctl/compose/docker", "1m", []v1alpha1.FileOperation{v1alpha1.FileOperationCreate, v1alpha1.FileOperationUpdate}),
			},
		},
		{
			Name:        lo.ToPtr("podman compose up"),
			Path:        lo.ToPtr("/var/run/flightctl/compose/podman"),
			Description: lo.ToPtr("Bring up a multi-container system based on the provided YAML podman-compose definition file"),
			Actions: []v1alpha1.HookAction{
				marshalExecutable("podman-compose -f {{ .FilePath }} up -d", nil,
					"/var/run/flightctl/compose/podman", "1m", []v1alpha1.FileOperation{v1alpha1.FileOperationCreate, v1alpha1.FileOperationUpdate}),
			},
		},
		{
			Name:        lo.ToPtr("quadlet up"),
			Path:        lo.ToPtr("/etc/containers/systemd"),
			Description: lo.ToPtr("Bring up a quadlet systemd unit based on the provided quadlet definition file"),
			Actions: []v1alpha1.HookAction{
				marshalExecutable(`systemctl daemon-reload && fname=$(basename {{ .FilePath }}) && srv=$([[ "${fname##*.}" == "container" ]] && echo -n "${fname%.*}" || echo -n ${fname//./-}).service && systemctl start $srv`, nil,
					"/etc/containers/systemd", "1m", []v1alpha1.FileOperation{v1alpha1.FileOperationCreate, v1alpha1.FileOperationUpdate}),
			},
		},
		{
			Name:        lo.ToPtr("quadlet cleanup"),
			Path:        lo.ToPtr("/etc/containers/systemd"),
			Description: lo.ToPtr("Reorganize systemd cache after quadlet file removal"),
			Actions: []v1alpha1.HookAction{
				marshalExecutable(`systemctl daemon-reload`, nil,
					"/etc/containers/systemd", "1m", []v1alpha1.FileOperation{v1alpha1.FileOperationRemove}),
			},
		},
	}
}

func defaultBeforeUpdateHooks() []v1alpha1.DeviceHookSpec {
	return []v1alpha1.DeviceHookSpec{
		{
			Name:        lo.ToPtr("docker compose down"),
			Path:        lo.ToPtr("/var/run/flightctl/compose/docker"),
			Description: lo.ToPtr("Bring down a multi-container system based on the provided YAML docker-compose definition file"),
			Actions: []v1alpha1.HookAction{
				marshalExecutable("docker-compose -f {{ .FilePath }} down", lo.ToPtr([]string{"DOCKER_HOST=unix:///run/podman/podman.sock"}),
					"/var/run/flightctl/compose/docker", "1m", []v1alpha1.FileOperation{v1alpha1.FileOperationUpdate, v1alpha1.FileOperationRemove}),
			},
		},
		{
			Name:        lo.ToPtr("podman compose down"),
			Path:        lo.ToPtr("/var/run/flightctl/compose/podman"),
			Description: lo.ToPtr("Bring down a multi-container system based on the provided YAML podman-compose definition file"),
			Actions: []v1alpha1.HookAction{
				marshalExecutable("podman-compose -f {{ .FilePath }} down", nil,
					"/var/run/flightctl/compose/podman", "1m", []v1alpha1.FileOperation{v1alpha1.FileOperationUpdate, v1alpha1.FileOperationRemove}),
			},
		},
		{
			Name:        lo.ToPtr("quadlet down"),
			Path:        lo.ToPtr("/etc/containers/systemd"),
			Description: lo.ToPtr("Bring down a quadlet systemd unit based on the provided quadlet definition file"),
			Actions: []v1alpha1.HookAction{
				marshalExecutable(`fname=$(basename {{ .FilePath }}) && srv=$([[ "${fname##*.}" == "container" ]] && echo -n "${fname%.*}" || echo -n ${fname//./-}).service && systemctl stop $srv`, nil,
					"/etc/containers/systemd", "1m", []v1alpha1.FileOperation{v1alpha1.FileOperationUpdate, v1alpha1.FileOperationRemove}),
			},
		},
	}
}
