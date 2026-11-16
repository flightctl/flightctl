package v1beta1

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/getkin/kin-openapi/openapi3"
)

var registryScopeRegexp = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)

// ValidateConfigResources validates rendered intent and collection-wide identities.
func ValidateConfigResources(resources []ConfigResource) []error {
	var errs []error
	names, endpoints := map[string]bool{}, map[string]bool{}
	for i, resource := range resources {
		for _, err := range resource.Validate() {
			errs = append(errs, fmt.Errorf("configResources[%d]: %w", i, err))
		}
		registry, err := resource.AsRegistryConfigResource()
		if err != nil {
			continue
		}
		if names[registry.Name] {
			errs = append(errs, fmt.Errorf("configResources[%d].name must be unique", i))
		}
		if endpoints[registry.Registry] {
			errs = append(errs, fmt.Errorf("configResources[%d].registry must be unique", i))
		}
		names[registry.Name], endpoints[registry.Registry] = true, true
	}
	return errs
}

func (resource ConfigResource) Validate() []error {
	kind, err := resource.Discriminator()
	if err != nil || kind != "registry" {
		return []error{errors.New("type must be registry")}
	}
	registry, err := resource.AsRegistryConfigResource()
	if err != nil {
		return []error{errors.New("registry resource has invalid field types")}
	}
	errs := registry.Validate()
	var value any
	if err := json.Unmarshal(resource.union, &value); err != nil {
		return append(errs, errors.New("registry resource must be a JSON object"))
	}
	spec, err := GetSpec()
	if err != nil {
		return append(errs, errors.New("configuration resource schema unavailable"))
	}
	if err := spec.Components.Schemas["ConfigResource"].Value.VisitJSON(value); err != nil {
		var schemaErr *openapi3.SchemaError
		path := "resource"
		if errors.As(err, &schemaErr) && len(schemaErr.JSONPointer()) > 0 {
			path = strings.Join(schemaErr.JSONPointer(), ".")
		}
		errs = append(errs, fmt.Errorf("%s does not match the configuration resource schema", path))
	}
	return errs
}

func (resource RegistryConfigResource) Validate() []error {
	var errs []error
	add := func(field, reason string) { errs = append(errs, fmt.Errorf("%s %s", field, reason)) }
	if resource.Type != Registry {
		add("type", "must be registry")
	}
	if resource.Name == "" || resource.Name != "registry:"+resource.Registry {
		add("name", "must equal registry: followed by the normalized endpoint")
	}
	if resource.Registry != strings.ToLower(resource.Registry) || len(validation.ValidateHostIPOrFQDNWithOptionalPort(&resource.Registry, "registry")) != 0 {
		add("registry", "must be a normalized host with an optional valid port")
	}
	if resource.Scheme != RegistrySchemeHTTP && resource.Scheme != RegistrySchemeHTTPS {
		add("scheme", "must be https or http")
	}
	if resource.SkipServerVerification == nil {
		add("skipServerVerification", "is required")
	}
	if len(resource.Access) == 0 {
		add("access", "must be nonempty")
	}
	for i, access := range resource.Access {
		path := fmt.Sprintf("access[%d]", i)
		if access.Scope == nil {
			add(path+".scope", "is required")
		} else if *access.Scope != "" && !registryScopeRegexp.MatchString(*access.Scope) {
			add(path+".scope", "must be a relative OCI repository path or the empty string")
		}
		if len(access.Consumers) == 0 {
			add(path+".consumers", "must be nonempty")
		}
		for j, consumer := range access.Consumers {
			switch consumer.Type {
			case Container, HelmChart, HelmImage, Os:
			default:
				add(fmt.Sprintf("%s.consumers[%d].type", path, j), "must be container, helm-chart, helm-image or os")
			}
		}
		if access.Authentication != nil {
			kind, err := access.Authentication.Discriminator()
			auth, decodeErr := access.Authentication.AsDockerAuth()
			if err != nil || kind != "docker" || decodeErr != nil {
				add(path+".authentication", "must be valid docker authentication")
			} else {
				if len(auth.Username) < 1 || len(auth.Username) > 256 {
					add(path+".authentication.username", "must contain 1 to 256 characters")
				}
				if len(auth.Password) < 1 || len(auth.Password) > 8192 {
					add(path+".authentication.password", "must contain 1 to 8192 characters")
				}
			}
		}
	}
	if resource.CaCrt != nil && !validRegistryCABundle(*resource.CaCrt) {
		add("ca.crt", "must be a base64-encoded PEM certificate bundle")
	}
	return errs
}

func validRegistryCABundle(encoded string) bool {
	if len(encoded) == 0 || len(encoded) > maxBase64CertificateLength {
		return false
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	count := 0
	for data = bytes.TrimSpace(data); len(data) > 0; data = bytes.TrimSpace(data) {
		if !bytes.HasPrefix(data, []byte("-----BEGIN CERTIFICATE-----")) {
			return false
		}
		block, rest := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) > 0 {
			return false
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return false
		}
		count++
		data = rest
	}
	return count > 0
}
