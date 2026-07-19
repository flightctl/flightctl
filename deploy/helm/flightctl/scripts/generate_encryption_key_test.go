package scripts_test

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEncryptionKeyGeneration(t *testing.T) {
	scriptPath := encryptionKeyScriptPath()

	t.Run("When generating encryption key it should create a valid AES-256 key", func(t *testing.T) {
		encDir := t.TempDir()

		cmd := exec.Command("bash", scriptPath,
			"--encryption-dir", encDir,
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Script failed: %v\nOutput: %s", err, output)
		}

		encKeyFile := filepath.Join(encDir, "key")
		keyData, err := os.ReadFile(encKeyFile)
		if err != nil {
			t.Fatalf("Encryption key file was not created: %v", err)
		}

		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyData)))
		if err != nil {
			t.Fatalf("Encryption key is not valid base64: %v", err)
		}

		if len(decoded) != 32 {
			t.Errorf("Encryption key should be 32 bytes (AES-256), got %d", len(decoded))
		}
	})

	t.Run("When --encryption-dir is missing it should exit with an error", func(t *testing.T) {
		cmd := exec.Command("bash", scriptPath)
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("Script should have failed without --encryption-dir, output: %s", output)
		}
		if !strings.Contains(string(output), "--encryption-dir is required") {
			t.Errorf("Expected missing-arg error message, got: %s", output)
		}
	})

	t.Run("When encryption key exists it should skip regeneration", func(t *testing.T) {
		encDir := t.TempDir()

		cmd := exec.Command("bash", scriptPath,
			"--encryption-dir", encDir,
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("First run failed: %v\nOutput: %s", err, output)
		}

		encKeyFile := filepath.Join(encDir, "key")
		origKey, err := os.ReadFile(encKeyFile)
		if err != nil {
			t.Fatalf("Failed to read encryption key: %v", err)
		}

		cmd = exec.Command("bash", scriptPath,
			"--encryption-dir", encDir,
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Second run failed: %v\nOutput: %s", err, output)
		}

		newKey, err := os.ReadFile(encKeyFile)
		if err != nil {
			t.Fatalf("Failed to read encryption key after second run: %v", err)
		}

		if string(origKey) != string(newKey) {
			t.Error("Encryption key was regenerated when it should have been preserved")
		}

		if !strings.Contains(string(output), "already exists") {
			t.Error("Second run should indicate key already exists")
		}
	})
}

func encryptionKeyScriptPath() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "generate-encryption-key.sh")
}
