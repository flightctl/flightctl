package tasks

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

const (
	usernsConfiguredEnv = "_CONTAINERS_USERNS_CONFIGURED"
)

// isRootlessRuntime mirrors the checks Podman uses to distinguish a rootful
// process from root running inside a user namespace. Checking only the
// effective UID is insufficient for rootless Podman containers, whose process
// commonly sees UID 0 even though that UID maps to the invoking host user.
func isRootlessRuntime() bool {
	if os.Geteuid() != 0 || os.Getenv(usernsConfiguredEnv) != "" {
		return true
	}

	uidMap, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return false
	}
	lines := strings.Split(strings.TrimSpace(string(uidMap)), "\n")
	if len(lines) == 0 {
		return false
	}
	fields := strings.Fields(lines[0])
	if len(fields) != 3 || fields[0] != "0" || fields[1] != "0" || fields[2] != "4294967295" {
		return true
	}
	return false
}

func mergeCommandEnv(overrides map[string]string) []string {
	env := os.Environ()
	for key, value := range overrides {
		prefix := key + "="
		filtered := env[:0]
		for _, item := range env {
			if !strings.HasPrefix(item, prefix) {
				filtered = append(filtered, item)
			}
		}
		env = append(filtered, prefix+value)
	}
	return env
}

func podmanCommandWithRuntime(ctx context.Context, rootless bool, runtimeDir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "podman", args...)
	if rootless {
		overrides := map[string]string{"XDG_DATA_HOME": "/var/lib"}
		if runtimeDir != "" {
			overrides["XDG_RUNTIME_DIR"] = runtimeDir
		}
		cmd.Env = mergeCommandEnv(overrides)
	}
	return cmd
}

func (w *podmanWorker) podmanEnvironment(overrides map[string]string) []string {
	values := make(map[string]string, len(overrides)+1)
	for key, value := range overrides {
		values[key] = value
	}
	if w.Rootless && w.StorageConfigPath != "" {
		values["CONTAINERS_STORAGE_CONF"] = w.StorageConfigPath
	}
	if w.Rootless {
		values["XDG_DATA_HOME"] = "/var/lib"
	}
	if w.Rootless && w.PodmanRuntimeDir != "" {
		values["XDG_RUNTIME_DIR"] = w.PodmanRuntimeDir
	}
	return mergeCommandEnv(values)
}

// podmanCommand executes Podman in the imagebuilder worker's own runtime. In
// rootless mode that runtime is the build engine itself, with an isolated
// per-job storage config. The rootful path uses the existing behavior.
func (w *podmanWorker) podmanCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := podmanCommandWithRuntime(ctx, w.Rootless, w.PodmanRuntimeDir, args...)
	if w.Rootless {
		cmd.Env = w.podmanEnvironment(nil)
	}
	return cmd
}

func (w *podmanWorker) authFilePath() string {
	if w.Rootless {
		return w.AuthFilePath
	}
	return containerAuthFile
}

func (w *podmanWorker) authFileEnvironment() map[string]string {
	return map[string]string{"REGISTRY_AUTH_FILE": w.authFilePath()}
}

func (w *podmanWorker) registryCertDir(registryHostname string) string {
	return w.RegistryCertDirs[registryHostname]
}

// workerContainerPath maps the paths used by the rootful helper container to
// the worker pod's filesystem for the direct rootless Podman execution path.
func (w *podmanWorker) workerContainerPath(path string) string {
	if !w.Rootless {
		return path
	}
	switch {
	case path == "/build":
		return w.TmpDir
	case strings.HasPrefix(path, "/build/"):
		return w.TmpDir + strings.TrimPrefix(path, "/build")
	case path == "/output":
		return w.TmpOutDir
	case strings.HasPrefix(path, "/output/"):
		return w.TmpOutDir + strings.TrimPrefix(path, "/output")
	default:
		return path
	}
}
