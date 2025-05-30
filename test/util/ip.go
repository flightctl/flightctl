package util

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
	return filepath.Join(GetTopLevelDir(), "/test/data/examples/", yamlName)
}

func GetExtIP() string {
	// execute the test/scripts/get_ext_ip.sh script to get the external IP
	// of the host machine
	cmd := exec.Command(GetScriptPath("/get_ext_ip.sh")) //nolint:gosec
	output, err := cmd.Output()
	Expect(err).ToNot(HaveOccurred())
	return strings.TrimSpace(string(output))
}

func BinaryExistsOnPath(binaryName string) bool {
	_, err := exec.LookPath(binaryName)
	return err == nil
}
