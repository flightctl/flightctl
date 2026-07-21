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
		binDir, storeDir := fakeKubectlEnv(t)
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
		binDir, storeDir := fakeKubectlEnv(t)
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
		binDir, storeDir := fakeKubectlEnv(t)
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
}

type fakeSecret struct {
	Data        map[string]string `json:"data"`
	Annotations map[string]string `json:"annotations"`
	Labels      map[string]string `json:"labels"`
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

func fakeKubectlEnv(t *testing.T) (binDir, storeDir string) {
	t.Helper()
	storeDir = t.TempDir()
	binDir = t.TempDir()

	// Fake kubectl that stores Secrets as JSON files. Supports the subset used by
	// ensure-password-secrets.sh: get/create|apply/annotate/label.
	simple := fmt.Sprintf(`#!/usr/bin/env python3
import base64, json, os, sys

STORE = %q

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
        # create secret generic NAME --namespace NS --from-literal=k=v --dry-run=client -o yaml
        name = ns = None
        literals = []
        i = 0
        while i < len(args):
            a = args[i]
            if a in ("secret", "generic"):
                i += 1
            elif a == "--namespace":
                ns = args[i+1]; i += 2
            elif a.startswith("--namespace="):
                ns = a.split("=", 1)[1]; i += 1
            elif a == "--from-literal":
                literals.append(args[i+1]); i += 2
            elif a.startswith("--from-literal="):
                literals.append(a.split("=", 1)[1]); i += 1
            elif a in ("--dry-run=client", "-o", "yaml"):
                i += 1
            else:
                name = a; i += 1
        data = {}
        for item in literals:
            k, v = item.split("=", 1)
            data[k] = base64.b64encode(v.encode()).decode()
        obj = {
            "metadata": {"name": name, "namespace": ns},
            "data": data,
            "annotations": {},
            "labels": {},
        }
        # Print JSON for apply -f -
        print(json.dumps(obj))
        return

    if cmd == "apply":
        # apply -f -   (stdin); ignore -f/- flags
        raw = sys.stdin.read().strip()
        if not raw:
            return
        # Some kubectl builds emit YAML; our create dry-run prints JSON.
        obj = json.loads(raw)
        existing = load(obj["metadata"]["namespace"], obj["metadata"]["name"]) or {
            "data": {}, "annotations": {}, "labels": {}, "metadata": obj["metadata"]
        }
        existing["data"] = obj.get("data", existing.get("data", {}))
        existing["metadata"] = obj["metadata"]
        existing.setdefault("annotations", {})
        existing.setdefault("labels", {})
        save(existing)
        print(f"secret/{obj['metadata']['name']} configured")
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
`, storeDir)

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
