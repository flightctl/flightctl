package satellite

import (
	"fmt"
	"os"
	"path/filepath"
)

func getProjectRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	for _, relPath := range []string{"../../..", "../..", ".."} {
		absPath, err := filepath.Abs(filepath.Join(cwd, relPath))
		if err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(absPath, "go.mod")); err == nil {
			return absPath, nil
		}
	}
	return "", fmt.Errorf("could not find project root from %s", cwd)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
