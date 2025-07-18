package config

import (
	"fmt"
	"slices"
)

const (
	TPMPersistenceTypeFixedHandle = "fixed-handle"
	TPMPersistenceTypeKeyBlob     = "key-blob"
	TPMPersistenceTypeAutoHandle  = "auto-handle"
	TPMPersistenceTypeNone        = "none"
)

var TPMPersistenceTypes = []string{
	TPMPersistenceTypeFixedHandle,
	TPMPersistenceTypeKeyBlob,
	TPMPersistenceTypeAutoHandle,
	TPMPersistenceTypeNone,
}

// TPMConfig holds all TPM-related configuration
type TPMConfig struct {
	// Path is the path to the TPM device
	Path string `json:"tpm-path,omitempty"`
	// PersistenceMetadata specifies the location/handle for TPM key persistence
	PersistenceMetadata string `json:"tpm-persistence-metadata,omitempty"`
	// PersistenceType specifies how the TPM key should be persisted
	PersistenceType string `json:"tpm-persistence-type,omitempty"`
}

// Validate checks that the TPM configuration is valid
func (t *TPMConfig) Validate() error {
	if t.PersistenceType != "" {
		if !slices.Contains(TPMPersistenceTypes, t.PersistenceType) {
			return fmt.Errorf("TPM persistence type %q must be one of: %v", t.PersistenceType, TPMPersistenceTypes)
		}
		// Metadata is not required for "none" type since keys are ephemeral
		if t.PersistenceType != TPMPersistenceTypeNone && t.PersistenceMetadata == "" {
			return fmt.Errorf("TPM persistence metadata is required when a TPM persistence type is selected")
		}
	}
	return nil
}

// MergeWith merges override TPM configuration into this configuration
func (t *TPMConfig) MergeWith(override *TPMConfig) {
	overrideIfNotEmpty(&t.Path, override.Path)
	overrideIfNotEmpty(&t.PersistenceType, override.PersistenceType)
	overrideIfNotEmpty(&t.PersistenceMetadata, override.PersistenceMetadata)
}
