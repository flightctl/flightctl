package model

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
)

// EncryptionHandlers maps model type name to its encryption handler.
// This registry is used by the GORM encryption plugin to know how to encrypt each model.
func EncryptionHandlers() map[string]encryption.ModelEncryptHandler {
	return map[string]encryption.ModelEncryptHandler{
		domain.RepositoryKind:   encryptRepository,
		domain.AuthProviderKind: encryptAuthProvider,
	}
}

// repositoryEncryptPaths lists the sensitive fields in the Repository Spec JSONB
// that must be encrypted at rest.
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

	updated, err := encryptJSONField(ctx, repo.Spec.Data, repositoryEncryptPaths, encrypt)
	if err != nil {
		return err
	}
	// Unmarshal into a new object — Spec.Data shares a pointer with the API
	// resource, and mutating in-place corrupts its json.RawMessage on retry.
	if updated != nil {
		var newSpec domain.RepositorySpec
		if err := json.Unmarshal(updated, &newSpec); err != nil {
			return fmt.Errorf("unmarshal encrypted repo spec: %w", err)
		}
		repo.Spec.Data = newSpec
	}
	return nil
}

func encryptAuthProvider(ctx context.Context, v interface{}, encrypt encryption.EncryptFunc) error {
	ap, ok := v.(*AuthProvider)
	if !ok {
		return fmt.Errorf("expected *AuthProvider, got %T", v)
	}

	if ap.Spec == nil {
		return nil
	}

	updated, err := encryptJSONField(ctx, ap.Spec.Data, authProviderEncryptPaths, encrypt)
	if err != nil {
		return err
	}
	// See comment in encryptRepository for why we unmarshal into a new object.
	if updated != nil {
		var newSpec domain.AuthProviderSpec
		if err := json.Unmarshal(updated, &newSpec); err != nil {
			return fmt.Errorf("unmarshal encrypted auth provider spec: %w", err)
		}
		ap.Spec.Data = newSpec
	}
	return nil
}

// encryptJSONField encrypts string values at the given paths within a JSONB field.
// It marshals the data to a generic map, encrypts matching paths, and returns the
// updated JSON bytes. Returns nil if no fields were modified. The caller is
// responsible for unmarshaling into a new object to avoid corrupting shared pointers.
func encryptJSONField(ctx context.Context, data any, paths [][]string, encrypt encryption.EncryptFunc) ([]byte, error) {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal spec: %w", err)
	}

	var jsonData map[string]any
	if err := json.Unmarshal(jsonBytes, &jsonData); err != nil {
		return nil, fmt.Errorf("unmarshal spec to map: %w", err)
	}

	modified := false
	for _, path := range paths {
		encrypted, err := encryptJSONPath(ctx, jsonData, path, encrypt)
		if err != nil {
			return nil, fmt.Errorf("encrypt field %v: %w", path, err)
		}
		if encrypted {
			modified = true
		}
	}

	if !modified {
		return nil, nil
	}

	updatedJSON, err := json.Marshal(jsonData)
	if err != nil {
		return nil, fmt.Errorf("marshal updated spec: %w", err)
	}

	return updatedJSON, nil
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
