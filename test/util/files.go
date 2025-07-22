package util

import (
	"crypto/md5" //nolint:gosec
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func GetTopLevelDir() string {
	pwd := os.Getenv("PWD")
	// split path parts
	parts := strings.Split(pwd, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "test" {
			path := strings.Join(parts[:i], "/")
			return path
		}
	}
	Fail("Could not find top-level directory")
	return ""
}

func GetScriptPath(script string) string {
	scriptsDir := GetTopLevelDir() + "/test/scripts/" + script
	return scriptsDir
}

func GetTestExamplesYamlPath(yamlName string) string {
	if yamlName == "" {
		return ""
	}
	dir := ensureTestSpecificYamlsRendered(CurrentSpecReport().FullText())
	return filepath.Join(dir, yamlName)
}

type TestHash = string

// Ginkgo runs each parallel test in a separate process so this map should be safe to access
// without a mutex as long as the tests themselves aren't running multiple goroutines accessing
// test yamls.
var yamlRenderDir = make(map[TestHash]string)

func ensureTestSpecificYamlsRendered(key string) string {
	testHash := fmt.Sprintf("%x", md5.Sum([]byte(key))) //nolint:gosec

	if _, ok := yamlRenderDir[testHash]; !ok {
		renderDir := GinkgoT().TempDir()

		err := renderTestExamples(testHash, renderDir)
		Expect(err).ToNot(HaveOccurred())

		yamlRenderDir[testHash] = renderDir
	}

	return yamlRenderDir[testHash]
}

func renderTestExamples(testHash TestHash, renderDir string) error {
	yamlFiles, err := filepath.Glob(filepath.Join(GetTopLevelDir(), "/test/data/examples/*.yaml"))
	if err != nil {
		return err
	}

	for _, f := range yamlFiles {
		t, err := template.ParseFiles(f)
		if err != nil {
			return err
		}

		outFile, err := os.Create(filepath.Join(renderDir, filepath.Base(f)))
		if err != nil {
			return err
		}

		if err := t.Execute(outFile, map[string]string{"TestHash": testHash}); err != nil {
			return err
		}

		if err := outFile.Close(); err != nil {
			return fmt.Errorf("Failed to close yaml file: %w", err)
		}
	}
	return nil
}

func BinaryExistsOnPath(binaryName string) bool {
	_, err := exec.LookPath(binaryName)
	return err == nil
}
