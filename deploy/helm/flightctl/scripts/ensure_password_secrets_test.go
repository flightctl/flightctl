package scripts_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnsurePasswordSecrets(t *testing.T) {
	scriptPath := ensurePasswordSecretsScriptPath()

	t.Run("When required flags are missing it should fail", func(t *testing.T) {
		cmd := exec.Command("bash", scriptPath)
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected failure without flags, got: %s", output)
		}
		if !strings.Contains(string(output), "--namespace is required") {
			t.Errorf("expected namespace error, got: %s", output)
		}
	})

	t.Run("When ensure flags are missing it should fail", func(t *testing.T) {
		cmd := exec.Command("bash", scriptPath, "--namespace", "flightctl")
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected failure without ensure flags, got: %s", output)
		}
		if !strings.Contains(string(output), "at least one --ensure-* flag is required") {
			t.Errorf("expected ensure-flag error, got: %s", output)
		}
	})

	t.Run("When secrets are missing it should create them with keep annotations", func(t *testing.T) {
		binDir, storeDir := fakeKubectlEnv(t, fakeKubectlOptions{})
		cmd := exec.Command("bash", scriptPath,
			"--namespace", "flightctl",
			"--ensure-db-admin",
			"--ensure-kv",
		)
		cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("script failed: %v\nOutput: %s", err, output)
		}

		admin := readFakeSecret(t, storeDir, "flightctl", "flightctl-db-admin-secret")
		if got := string(admin.Data["masterUser"]); got != base64.StdEncoding.EncodeToString([]byte("admin")) {
			t.Errorf("unexpected masterUser: %s", got)
		}
		if admin.Data["masterPassword"] == "" {
			t.Error("expected masterPassword to be set")
		}
		assertKeepAnnotations(t, admin)

		kv := readFakeSecret(t, storeDir, "flightctl", "flightctl-kv-secret")
		if kv.Data["password"] == "" {
			t.Error("expected kv password to be set")
		}
		assertKeepAnnotations(t, kv)
	})

	t.Run("When secrets already exist it should preserve passwords and annotate", func(t *testing.T) {
		binDir, storeDir := fakeKubectlEnv(t, fakeKubectlOptions{})
		existingPassword := base64.StdEncoding.EncodeToString([]byte("keep-me-password"))
		writeFakeSecret(t, storeDir, "flightctl", "flightctl-kv-secret", map[string]string{
			"password": existingPassword,
		})

		cmd := exec.Command("bash", scriptPath,
			"--namespace", "flightctl",
			"--ensure-kv",
		)
		cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("script failed: %v\nOutput: %s", err, output)
		}
		if !strings.Contains(string(output), "preserving data") {
			t.Errorf("expected preserve message, got: %s", output)
		}

		kv := readFakeSecret(t, storeDir, "flightctl", "flightctl-kv-secret")
		if got := kv.Data["password"]; got != existingPassword {
			t.Errorf("password rotated: got %q want %q", got, existingPassword)
		}
		assertKeepAnnotations(t, kv)
	})

	t.Run("When secret exists in one namespace it should copy password to the other", func(t *testing.T) {
		binDir, storeDir := fakeKubectlEnv(t, fakeKubectlOptions{})
		existingPassword := base64.StdEncoding.EncodeToString([]byte("shared-password"))
		writeFakeSecret(t, storeDir, "flightctl", "flightctl-db-app-secret", map[string]string{
			"user":         base64.StdEncoding.EncodeToString([]byte("flightctl_app")),
			"userPassword": existingPassword,
		})

		cmd := exec.Command("bash", scriptPath,
			"--namespace", "flightctl",
			"--internal-namespace", "flightctl-internal",
			"--ensure-db-app",
		)
		cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("script failed: %v\nOutput: %s", err, output)
		}

		primary := readFakeSecret(t, storeDir, "flightctl", "flightctl-db-app-secret")
		internal := readFakeSecret(t, storeDir, "flightctl-internal", "flightctl-db-app-secret")
		if primary.Data["userPassword"] != existingPassword {
			t.Errorf("primary password changed: %q", primary.Data["userPassword"])
		}
		if internal.Data["userPassword"] != existingPassword {
			t.Errorf("internal password mismatch: got %q want %q", internal.Data["userPassword"], existingPassword)
		}
		assertKeepAnnotations(t, primary)
		assertKeepAnnotations(t, internal)
	})

	t.Run("When lookup fails it should abort without creating secrets", func(t *testing.T) {
		binDir, storeDir := fakeKubectlEnv(t, fakeKubectlOptions{failGet: true})
		cmd := exec.Command("bash", scriptPath,
			"--namespace", "flightctl",
			"--ensure-kv",
		)
		cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected failure on lookup error, got: %s", output)
		}
		if !strings.Contains(string(output), "failed to look up secret") {
			t.Errorf("expected lookup error, got: %s", output)
		}
		if _, err := os.Stat(filepath.Join(storeDir, "flightctl", "flightctl-kv-secret.json")); err == nil {
			t.Error("secret should not be created when lookup fails")
		}
	})

	t.Run("When create races with AlreadyExists it should preserve not overwrite", func(t *testing.T) {
		binDir, storeDir := fakeKubectlEnv(t, fakeKubectlOptions{createAlreadyExists: true})
		existingPassword := base64.StdEncoding.EncodeToString([]byte("race-password"))
		// Secret appears only at create time via fake; seed for annotate/get after AlreadyExists.
		writeFakeSecret(t, storeDir, "flightctl", "flightctl-kv-secret", map[string]string{
			"password": existingPassword,
		})
		// Make get during find/exists report missing until create is attempted.
		if err := os.Rename(
			filepath.Join(storeDir, "flightctl", "flightctl-kv-secret.json"),
			filepath.Join(storeDir, "flightctl", "flightctl-kv-secret.json.hidden"),
		); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command("bash", scriptPath,
			"--namespace", "flightctl",
			"--ensure-kv",
		)
		cmd.Env = append(os.Environ(),
			"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"FAKE_REVEAL_ON_CREATE="+filepath.Join(storeDir, "flightctl", "flightctl-kv-secret.json.hidden"),
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("script failed: %v\nOutput: %s", err, output)
		}
		if !strings.Contains(string(output), "appeared concurrently") && !strings.Contains(string(output), "preserving data") {
			t.Errorf("expected race/preserve handling, got: %s", output)
		}
		kv := readFakeSecret(t, storeDir, "flightctl", "flightctl-kv-secret")
		if got := kv.Data["password"]; got != existingPassword {
			t.Errorf("password overwritten on race: got %q want %q", got, existingPassword)
		}
	})
}

type fakeSecret struct {
	Data        map[string]string `json:"data"`
	Annotations map[string]string `json:"annotations"`
	Labels      map[string]string `json:"labels"`
}

type fakeKubectlOptions struct {
	failGet            bool
	createAlreadyExists bool
}

func ensurePasswordSecretsScriptPath() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "ensure-password-secrets.sh")
}

func assertKeepAnnotations(t *testing.T, secret fakeSecret) {
	t.Helper()
	if secret.Annotations["helm.sh/resource-policy"] != "keep" {
		t.Errorf("missing helm keep annotation: %#v", secret.Annotations)
	}
	if secret.Annotations["argocd.argoproj.io/sync-options"] != "Prune=false" {
		t.Errorf("missing prune annotation: %#v", secret.Annotations)
	}
}

func fakeKubectlEnv(t *testing.T, opts fakeKubectlOptions) (binDir, storeDir string) {
	t.Helper()
	storeDir = t.TempDir()
	binDir = t.TempDir()

	simple := fmt.Sprintf(`#!/usr/bin/env python3
import base64, json, os, sys

STORE = %q
FAIL_GET = %s
CREATE_ALREADY_EXISTS = %s

def path(ns, name):
    return os.path.join(STORE, ns, f"{name}.json")

def load(ns, name):
    p = path(ns, name)
    if not os.path.exists(p):
        return None
    with open(p) as f:
        return json.load(f)

def save(obj):
    md = obj["metadata"]
    ns, name = md["namespace"], md["name"]
    os.makedirs(os.path.join(STORE, ns), exist_ok=True)
    with open(path(ns, name), "w") as f:
        json.dump(obj, f)

def main(argv):
    if not argv:
        raise SystemExit("missing command")
    cmd, args = argv[0], argv[1:]
    if cmd == "get":
        if FAIL_GET:
            print("Error: connection refused", file=sys.stderr)
            raise SystemExit(1)
        ns = name = output = None
        ignore = False
        i = 0
        while i < len(args):
            a = args[i]
            if a in ("secret", "secrets"):
                i += 1
            elif a in ("-n", "--namespace"):
                ns = args[i+1]; i += 2
            elif a == "--ignore-not-found":
                ignore = True; i += 1
            elif a == "-o":
                output = args[i+1]; i += 2
            else:
                name = a; i += 1
        obj = load(ns, name)
        if obj is None:
            if ignore:
                return
            raise SystemExit(f"not found: {ns}/{name}")
        if output == "name":
            print(f"secret/{name}")
            return
        if output and output.startswith("jsonpath={.data.") and output.endswith("}"):
            key = output[len("jsonpath={.data."):-1]
            print(obj.get("data", {}).get(key, ""), end="")
            return
        print(json.dumps(obj))
        return

    if cmd == "create":
        name = ns = None
        from_files = []
        i = 0
        while i < len(args):
            a = args[i]
            if a in ("secret", "generic"):
                i += 1
            elif a == "--namespace":
                ns = args[i+1]; i += 2
            elif a.startswith("--namespace="):
                ns = a.split("=", 1)[1]; i += 1
            elif a == "--from-file":
                from_files.append(args[i+1]); i += 2
            elif a.startswith("--from-file="):
                from_files.append(a.split("=", 1)[1]); i += 1
            else:
                name = a; i += 1
        reveal = os.environ.get("FAKE_REVEAL_ON_CREATE")
        if reveal and os.path.exists(reveal):
            os.rename(reveal, path(ns, name))
        if CREATE_ALREADY_EXISTS or load(ns, name) is not None:
            print(f'Error from server (AlreadyExists): secrets "{name}" already exists', file=sys.stderr)
            raise SystemExit(1)
        data = {}
        for item in from_files:
            k, fpath = item.split("=", 1)
            with open(fpath, "rb") as f:
                data[k] = base64.b64encode(f.read()).decode()
        obj = {
            "metadata": {"name": name, "namespace": ns},
            "data": data,
            "annotations": {},
            "labels": {},
        }
        save(obj)
        print(f"secret/{name} created")
        return

    if cmd == "annotate":
        ns = name = None
        anns = []
        i = 0
        while i < len(args):
            a = args[i]
            if a in ("secret", "secrets"):
                i += 1
            elif a in ("-n", "--namespace"):
                ns = args[i+1]; i += 2
            elif a == "--overwrite":
                i += 1
            elif name is None:
                name = a; i += 1
            else:
                anns.append(a); i += 1
        obj = load(ns, name)
        if obj is None:
            raise SystemExit(f"annotate: missing {ns}/{name}")
        obj.setdefault("annotations", {})
        for item in anns:
            k, v = item.split("=", 1)
            obj["annotations"][k] = v
        save(obj)
        return

    if cmd == "label":
        ns = name = None
        labels = []
        i = 0
        while i < len(args):
            a = args[i]
            if a in ("secret", "secrets"):
                i += 1
            elif a in ("-n", "--namespace"):
                ns = args[i+1]; i += 2
            elif a == "--overwrite":
                i += 1
            elif name is None and "=" not in a:
                name = a; i += 1
            else:
                labels.append(a); i += 1
        obj = load(ns, name)
        if obj is None:
            return
        obj.setdefault("labels", {})
        for item in labels:
            if "=" not in item:
                continue
            k, v = item.split("=", 1)
            obj["labels"][k] = v
        save(obj)
        return

    raise SystemExit(f"unsupported: {cmd} {' '.join(args)}")

if __name__ == "__main__":
    main(sys.argv[1:])
`, storeDir, map[bool]string{true: "True", false: "False"}[opts.failGet], map[bool]string{true: "True", false: "False"}[opts.createAlreadyExists])

	for _, name := range []string{"kubectl", "oc"} {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte(simple), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	return binDir, storeDir
}

func writeFakeSecret(t *testing.T, storeDir, ns, name string, data map[string]string) {
	t.Helper()
	dir := filepath.Join(storeDir, ns)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"metadata":    map[string]string{"name": name, "namespace": ns},
		"data":        data,
		"annotations": map[string]string{},
		"labels":      map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFakeSecret(t *testing.T, storeDir, ns, name string) fakeSecret {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(storeDir, ns, name+".json"))
	if err != nil {
		t.Fatalf("read secret %s/%s: %v", ns, name, err)
	}
	var raw struct {
		Data        map[string]string `json:"data"`
		Annotations map[string]string `json:"annotations"`
		Labels      map[string]string `json:"labels"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatal(err)
	}
	return fakeSecret{Data: raw.Data, Annotations: raw.Annotations, Labels: raw.Labels}
}
