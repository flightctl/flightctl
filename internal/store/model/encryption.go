package model

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
)

// EncryptionHandlers maps model type name to its encryption handler.
// This registry is used by the GORM encryption plugin to know how to encrypt each model.
func EncryptionHandlers() map[string]encryption.ModelEncryptHandler {
	return map[string]encryption.ModelEncryptHandler{
		"Repository":   encryptRepository,
		"AuthProvider": encryptAuthProvider,
	}
}

// repositoryEncryptPaths lists the sensitive fields in the Repository Spec JSONB
// that must be encrypted at rest. These match the fields redacted by HideSensitiveData
// in api/core/v1beta1/util.go.
// Each entry is a slice of JSON key segments (e.g. {"httpConfig", "tls.key"}).
var repositoryEncryptPaths = [][]string{
	{"httpConfig", "password"},
	{"httpConfig", "token"},
	{"httpConfig", "tls.key"},
	{"httpConfig", "tls.crt"},
	{"sshConfig", "sshPrivateKey"},
	{"sshConfig", "privateKeyPassphrase"},
	{"ociAuth", "password"},
}

// authProviderEncryptPaths lists the sensitive fields in the AuthProvider Spec JSONB.
// clientSecret appears at the top level of all provider type variants (OIDC, OAuth2,
// OpenShift, AAP).
var authProviderEncryptPaths = [][]string{
	{"clientSecret"},
}

func encryptRepository(ctx context.Context, v interface{}, encrypt encryption.EncryptFunc) error {
	repo, ok := v.(*Repository)
	if !ok {
		return fmt.Errorf("expected *Repository, got %T", v)
	}

	if repo.Spec == nil {
		return nil
	}

	return encryptJSONField(ctx, &repo.Spec.Data, repositoryEncryptPaths, encrypt)
}

func encryptAuthProvider(ctx context.Context, v interface{}, encrypt encryption.EncryptFunc) error {
	ap, ok := v.(*AuthProvider)
	if !ok {
		return fmt.Errorf("expected *AuthProvider, got %T", v)
	}

	if ap.Spec == nil {
		return nil
	}

	return encryptJSONField(ctx, &ap.Spec.Data, authProviderEncryptPaths, encrypt)
}

// encryptJSONField encrypts string values at the given paths within a JSONB field.
// It marshals the data to a generic map, encrypts matching paths, and unmarshals back.
func encryptJSONField(ctx context.Context, data any, paths [][]string, encrypt encryption.EncryptFunc) error {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal spec: %w", err)
	}

	var jsonData map[string]any
	if err := json.Unmarshal(jsonBytes, &jsonData); err != nil {
		return fmt.Errorf("unmarshal spec to map: %w", err)
	}

	modified := false
	for _, path := range paths {
		encrypted, err := encryptJSONPath(ctx, jsonData, path, encrypt)
		if err != nil {
			return fmt.Errorf("encrypt field %v: %w", path, err)
		}
		if encrypted {
			modified = true
		}
	}

	if !modified {
		return nil
	}

	updatedJSON, err := json.Marshal(jsonData)
	if err != nil {
		return fmt.Errorf("marshal updated spec: %w", err)
	}

	if err := json.Unmarshal(updatedJSON, data); err != nil {
		return fmt.Errorf("unmarshal to spec: %w", err)
	}

	return nil
}

// encryptJSONPath encrypts a string value at the given path segments in a JSON map.
// Returns true if encryption was performed, false if the path doesn't exist or the
// value was already encrypted with the current key.
func encryptJSONPath(ctx context.Context, data map[string]any, path []string, encrypt encryption.EncryptFunc) (bool, error) {
	current := data
	for i := 0; i < len(path)-1; i++ {
		val, exists := current[path[i]]
		if !exists {
			return false, nil
		}

		nestedMap, ok := val.(map[string]any)
		if !ok {
			return false, nil
		}

		current = nestedMap
	}

	lastPart := path[len(path)-1]
	val, exists := current[lastPart]
	if !exists {
		return false, nil
	}

	strVal, ok := val.(string)
	if !ok || strVal == "" {
		return false, nil
	}

	encrypted, err := encrypt(ctx, []byte(strVal))
	if err != nil {
		return false, fmt.Errorf("encrypt path %s: %w", path, err)
	}

	newValue := string(encrypted)
	if newValue == strVal {
		return false, nil
	}

	current[lastPart] = newValue
	return true, nil
}
