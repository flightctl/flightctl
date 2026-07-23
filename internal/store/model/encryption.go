package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/stoewer/go-strcase"
)

// EncryptedField defines a single sensitive field that must be encrypted at rest.
// Path segments use JSON key names from the struct root (e.g., {"Spec", "httpConfig", "password"}).
type EncryptedField struct {
	Kind string
	Path []string
}

// encryptionRegistry is the canonical list of all encrypted fields across all model types.
// The migration process can hash this list to detect when new fields are added between versions.
var encryptionRegistry = []EncryptedField{
	{domain.RepositoryKind, []string{"Spec", "httpConfig", "password"}},
	{domain.RepositoryKind, []string{"Spec", "httpConfig", "token"}},
	{domain.RepositoryKind, []string{"Spec", "httpConfig", "tls.key"}},
	{domain.RepositoryKind, []string{"Spec", "httpConfig", "tls.crt"}},
	{domain.RepositoryKind, []string{"Spec", "sshConfig", "sshPrivateKey"}},
	{domain.RepositoryKind, []string{"Spec", "sshConfig", "privateKeyPassphrase"}},
	{domain.RepositoryKind, []string{"Spec", "ociAuth", "password"}},
	{domain.AuthProviderKind, []string{"Spec", "clientSecret"}},
	{domain.DeviceKind, []string{"RenderedConfig"}},
	{domain.DeviceKind, []string{"RenderedApplications"}},
}

// encryptionPathsByKind is built from encryptionRegistry on init for O(1) lookup.
var encryptionPathsByKind map[string][][]string

func init() {
	encryptionPathsByKind = make(map[string][][]string)
	for _, ef := range encryptionRegistry {
		encryptionPathsByKind[ef.Kind] = append(encryptionPathsByKind[ef.Kind], ef.Path)
	}
}

// PathsForKind returns the encryption paths for a given model kind.
func PathsForKind(kind string) [][]string {
	return encryptionPathsByKind[kind]
}

// RegistryHash returns a SHA-256 hash of the encryptionRegistry.
// The migration process compares this hash across versions to detect when
// encrypted fields are added or removed, triggering a re-encryption migration.
func RegistryHash() string {
	entries := make([]string, len(encryptionRegistry))
	for i, ef := range encryptionRegistry {
		entries[i] = fmt.Sprintf("%s:%s", ef.Kind, strings.Join(ef.Path, "/"))
	}
	sort.Strings(entries)
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintln(h, e)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// EncryptionHandlers returns encryption handlers for all model types.
func EncryptionHandlers() map[string]encryption.ModelEncryptHandler {
	return map[string]encryption.ModelEncryptHandler{
		domain.RepositoryKind:   genericEncryptHandler(domain.RepositoryKind),
		domain.AuthProviderKind: genericEncryptHandler(domain.AuthProviderKind),
		domain.DeviceKind:       genericEncryptHandler(domain.DeviceKind),
	}
}

// genericEncryptHandler returns an encryption handler that works for any model type.
// For structs: marshal to nested map, encrypt at registered paths, unmarshal back.
// For maps (GORM partial updates): convert path segments from PascalCase to snake_case
// and encrypt *string values in-place.
func genericEncryptHandler(kind string) encryption.ModelEncryptHandler {
	paths := encryptionPathsByKind[kind]

	return func(ctx context.Context, model any, encrypt encryption.EncryptFunc) error {
		if len(paths) == 0 {
			return nil
		}

		if m, ok := model.(map[string]any); ok {
			return encryptMap(ctx, m, paths, encrypt)
		}

		jsonBytes, err := json.Marshal(model)
		if err != nil {
			return fmt.Errorf("marshal %s: %w", kind, err)
		}

		var data map[string]any
		dec := json.NewDecoder(bytes.NewReader(jsonBytes))
		dec.UseNumber()
		if err := dec.Decode(&data); err != nil {
			return fmt.Errorf("unmarshal %s to map: %w", kind, err)
		}

		modified := false
		for _, path := range paths {
			encrypted, err := encryptJSONPath(ctx, data, path, encrypt)
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

		modifiedBytes, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("marshal encrypted %s: %w", kind, err)
		}

		if err := json.Unmarshal(modifiedBytes, model); err != nil {
			return fmt.Errorf("unmarshal encrypted %s back to struct: %w", kind, err)
		}

		return nil
	}
}

// encryptMap handles GORM partial updates where model is a map[string]interface{}.
// Map keys use snake_case DB column names, so the first path segment is converted
// from PascalCase. Paths sharing the same top-level key are grouped so nested
// values (json.Unmarshaler) get a single marshal/unmarshal cycle per key.
func encryptMap(ctx context.Context, m map[string]any, paths [][]string, encrypt encryption.EncryptFunc) error {
	// Group paths by column key so nested values are marshaled once per key.
	grouped := make(map[string][][]string)
	var order []string
	for _, path := range paths {
		key := pascalToSnake(path[0])
		if _, seen := grouped[key]; !seen {
			order = append(order, key)
		}
		grouped[key] = append(grouped[key], path)
	}

	for _, columnKey := range order {
		val, ok := m[columnKey]
		if !ok || val == nil {
			continue
		}

		switch v := val.(type) {
		case *string:
			if v == nil || *v == "" {
				continue
			}
			// The value may be JSON-quoted from a prior encrypt pass
			// (e.g. "enc:v1:default:..."). Unwrap so ProcessEncryption
			// sees the raw enc: prefix and returns it as-is.
			plain := *v
			var unquoted string
			if json.Unmarshal([]byte(plain), &unquoted) == nil {
				plain = unquoted
			}
			encrypted, err := encrypt(ctx, []byte(plain))
			if err != nil {
				return fmt.Errorf("encrypt map key %q: %w", columnKey, err)
			}
			encStr := string(encrypted)
			if encStr == plain {
				continue
			}
			jsonStr, err := json.Marshal(encStr)
			if err != nil {
				return fmt.Errorf("marshal encrypted value for %q: %w", columnKey, err)
			}
			*v = string(jsonStr)

		case json.Unmarshaler:
			jsonBytes, err := json.Marshal(v)
			if err != nil {
				return fmt.Errorf("marshal map key %q for nested encrypt: %w", columnKey, err)
			}
			var nested map[string]any
			if err := json.Unmarshal(jsonBytes, &nested); err != nil {
				return fmt.Errorf("unmarshal map key %q to nested map: %w", columnKey, err)
			}
			modified := false
			for _, path := range grouped[columnKey] {
				encrypted, err := encryptJSONPath(ctx, nested, path[1:], encrypt)
				if err != nil {
					return fmt.Errorf("encrypt nested field %v: %w", path, err)
				}
				if encrypted {
					modified = true
				}
			}
			if !modified {
				continue
			}
			modifiedBytes, err := json.Marshal(nested)
			if err != nil {
				return fmt.Errorf("marshal encrypted nested %q: %w", columnKey, err)
			}
			if err := v.UnmarshalJSON(modifiedBytes); err != nil {
				return fmt.Errorf("unmarshal encrypted nested %q back: %w", columnKey, err)
			}

		default:
			return fmt.Errorf("encrypt map key %q: unsupported type %T", columnKey, val)
		}
	}
	return nil
}

// pascalToSnake converts a PascalCase string to snake_case.
// e.g. "RenderedConfig" → "rendered_config", "Spec" → "spec"
func pascalToSnake(s string) string {
	return strcase.SnakeCase(s)
}

// encryptJSONPath encrypts the value at the given path in a JSON map.
// If the leaf is a string, it encrypts the string directly.
// If the leaf is anything else (map, array, etc.), it marshals the value to JSON bytes,
// encrypts the blob, and stores the encrypted string back.
func encryptJSONPath(ctx context.Context, data map[string]any, path []string, encrypt encryption.EncryptFunc) (bool, error) {
	current := data
	for i := 0; i < len(path)-1; i++ {
		val, exists := current[path[i]]
		if !exists || val == nil {
			return false, nil
		}
		nestedMap, ok := val.(map[string]any)
		if !ok {
			return false, fmt.Errorf("path %v: segment %q is %T, expected object", path, path[i], val)
		}
		current = nestedMap
	}

	lastKey := path[len(path)-1]
	val, exists := current[lastKey]
	if !exists || val == nil {
		return false, nil
	}

	var plaintext []byte

	switch v := val.(type) {
	case string:
		if v == "" {
			return false, nil
		}
		plaintext = []byte(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return false, fmt.Errorf("marshal blob at %v: %w", path, err)
		}
		plaintext = b
	}

	encrypted, err := encrypt(ctx, plaintext)
	if err != nil {
		return false, fmt.Errorf("encrypt path %v: %w", path, err)
	}

	newValue := string(encrypted)
	if newValue == string(plaintext) {
		return false, nil
	}

	current[lastKey] = newValue
	return true, nil
}
