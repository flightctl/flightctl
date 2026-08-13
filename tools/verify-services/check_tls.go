package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func checkTLS(repoRoot string, services []ExpandedService) []Issue {
	const check = "tls"
	genPath := filepath.Join(repoRoot, "deploy/helm/flightctl/scripts/generate-certificates.sh")
	genData, err := os.ReadFile(genPath)
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	gen := string(genData)

	initPath := filepath.Join(repoRoot, "deploy/scripts/init_certs.sh")
	initData, err := os.ReadFile(initPath)
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	init := string(initData)

	opensslPath := filepath.Join(repoRoot, "deploy/helm/flightctl/templates/certs/flightctl-certs-from-openssl.yaml")
	opensslData, err := os.ReadFile(opensslPath)
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	openssl := string(opensslData)

	cmPath := filepath.Join(repoRoot, "deploy/helm/flightctl/templates/certs/flightctl-certs-from-certmanager.yaml")
	cmData, err := os.ReadFile(cmPath)
	if err != nil {
		return []Issue{{Check: check, Message: err.Error()}}
	}
	cm := string(cmData)

	var issues []Issue
	for _, s := range services {
		if !s.NeedsTLS {
			continue
		}
		flag := "--" + s.CertSanFlag + "-san"
		if !strings.Contains(gen, flag) {
			issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: generate-certificates.sh missing %s", s.Name, flag)})
		}
		// Quadlet init_certs only when the service actually mounts its server cert from PKI.
		// Some external services terminate TLS at the gateway and speak plain HTTP upstream
		// (e.g. imagebuilder-api); those use Helm generate-certificates / cert-manager only.
		if s.Quadlet && quadletMountsServerCert(repoRoot, s.Name) && !strings.Contains(init, flag) {
			issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: init_certs.sh missing %s (quadlet mounts server cert)", s.Name, flag)})
		}
		if s.Helm {
			if !strings.Contains(openssl, flag) {
				issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: flightctl-certs-from-openssl.yaml missing %s", s.Name, flag)})
			}
			secret := "flightctl-" + s.Name + "-server-tls"
			if !strings.Contains(cm, secret) {
				issues = append(issues, Issue{Check: check, Message: fmt.Sprintf("%s: flightctl-certs-from-certmanager.yaml missing %s", s.Name, secret)})
			}
		}
	}
	return issues
}

func quadletMountsServerCert(repoRoot, name string) bool {
	dir := filepath.Join(repoRoot, "deploy/podman", "flightctl-"+name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	needle := "/etc/flightctl/pki/flightctl-" + name + "/server.crt"
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".container") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if strings.Contains(string(data), needle) {
			return true
		}
	}
	return false
}
