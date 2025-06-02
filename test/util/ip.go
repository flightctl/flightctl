package util

import (
	"fmt"
	"net"
	"net/url"
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

func resolveHostNameIP4Address(hostname string) (string, error) {
	if ip := net.ParseIP(hostname); ip != nil {
		return ip.String(), nil
	}
	// Take the first ipv4 address that matches that we find
	ips, err := net.LookupIP(hostname)
	if err != nil {
		return "", fmt.Errorf("failed to lookup IP '%s': %w", hostname, err)
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("hostname '%s' resolved to no IP addresses", hostname)
	}
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip.To4().String(), nil
		}
	}
	return "", fmt.Errorf("hostname '%s' resolved to no IPv4 addresses", hostname)
}

// ParseURIForIPAndPort parses a string URI and attempts to extract the IP address and port.
// It handles URIs with or without a scheme. If no port is specified, it attempts to
// deduce it from common schemes (http: 80, https: 443).
// If no scheme is supplied, http is assumed
func ParseURIForIPAndPort(rawURI string) (string, string, error) {
	// if no scheme exists default to http. scheme is required for url.Parse
	if !strings.Contains(rawURI, "://") {
		rawURI = "http://" + rawURI
	}

	parsedURL, err := url.Parse(rawURI)
	if err != nil {
		return "", "", fmt.Errorf("failed to parse URI '%s': %w", rawURI, err)
	}

	hostname := parsedURL.Hostname()
	if hostname == "" {
		return "", "", fmt.Errorf("no hostname found in URI '%s'", rawURI)
	}

	ip, err := resolveHostNameIP4Address(hostname)
	if err != nil {
		return "", "", err
	}

	// use the parsed port or default to a few well known ones if nothing is supplied
	port := parsedURL.Port()
	if port == "" {
		// If no port is specified, try to infer from scheme
		switch strings.ToLower(parsedURL.Scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", "", fmt.Errorf("no port specified in URI '%s' and scheme '%s' has no default port defined", rawURI, parsedURL.Scheme)
		}
	}
	return ip, port, nil
}
