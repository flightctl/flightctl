package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func checkNginx(repoRoot string, services []ExpandedService) []Issue {
	const check = "nginx"
	data, err := os.ReadFile(filepath.Join(repoRoot, "deploy/podman/flightctl-gateway/flightctl-gateway-config/nginx.conf.template"))
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	content := string(data)
	var issues []Issue
	for _, s := range services {
		if !s.RequireNginx {
			continue
		}
		host := "flightctl-" + s.Name
		if !strings.Contains(content, host) {
			issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: nginx.conf.template does not mention %q", s.Name, host)})
		}
	}
	return issues
}
