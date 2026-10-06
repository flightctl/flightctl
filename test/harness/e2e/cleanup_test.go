package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestContainerDeviceCleanup(t *testing.T) {
	scriptPath, err := filepath.Abs("../../../test/scripts/e2e_cleanup.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name    string
		session string
		host    string
	}{
		{"When the session matches it should remove session devices and old orphans", "current", "tcp://runtime.test:2375"},
		{"When the session is lost it should remove only old orphans", "", "tcp://runtime.test:2375"},
		{"When the session differs it should remove only old orphans", "other", "tcp://runtime.test:2375"},
		{"When using remote Podman it should use the selected endpoint", "", "unix:///tmp/test-podman.sock"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			binDir := t.TempDir()
			logPath := filepath.Join(binDir, "runtime.log")
			runtimeScript := `#!/bin/bash
printf '%s\n' "$*" >> "$RUNTIME_LOG"
if [[ "$1" == --remote ]]; then shift 3; else shift 2; fi
case "$1" in
    ps)
        case "$*" in
            *label=flightctl.e2e.session=current*) echo flightctl-e2e-container-current ;;
            *name=flightctl-e2e-container-*) printf '%s\n' flightctl-e2e-container-old flightctl-e2e-container-recent flightctl-e2e-container-unknown unrelated-flightctl-e2e-container-old ;;
        esac ;;
    inspect)
        case "${@: -1}" in
            flightctl-e2e-container-old) date --date='3 hours ago' --iso-8601=seconds ;;
            flightctl-e2e-container-recent) date --iso-8601=seconds ;;
            *) exit 1 ;;
        esac ;;
    rm) ;;
    *) exit 1 ;;
esac
`
			for name, content := range map[string]string{
				"docker": runtimeScript,
				"podman": runtimeScript,
				"virsh":  "#!/bin/sh\nexit 1\n",
				"find":   "#!/bin/sh\nexit 0\n",
			} {
				if err := os.WriteFile(filepath.Join(binDir, name), []byte(content), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RUNTIME_LOG", logPath)
			t.Setenv("DOCKER_HOST", testCase.host)
			t.Setenv("E2E_SESSION_ID", testCase.session)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if output, err := exec.CommandContext(ctx, "bash", scriptPath).CombinedOutput(); err != nil {
				t.Fatalf("cleanup failed: %v\n%s", err, output)
			}
			log, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			commands := string(log)
			if !strings.Contains(commands, "rm -f -v flightctl-e2e-container-old\n") {
				t.Fatalf("cleanup did not remove the old orphan: %s", commands)
			}
			for _, name := range []string{"flightctl-e2e-container-recent", "flightctl-e2e-container-unknown", "unrelated-flightctl-e2e-container-old"} {
				if strings.Contains(commands, "rm -f -v "+name+"\n") {
					t.Fatalf("cleanup removed protected container %s: %s", name, commands)
				}
			}
			removedSession := strings.Contains(commands, "rm -f -v flightctl-e2e-container-current\n")
			if removedSession != (testCase.session == "current") {
				t.Fatalf("session removal = %v for session %q: %s", removedSession, testCase.session, commands)
			}
			endpoint := "--host " + testCase.host
			if strings.Contains(testCase.host, "podman") {
				endpoint = "--remote --url " + testCase.host
			}
			for _, command := range strings.Split(strings.TrimSpace(commands), "\n") {
				if !strings.HasPrefix(command, endpoint+" ") {
					t.Fatalf("cleanup used the wrong runtime endpoint: %s", command)
				}
			}
		})
	}
}
