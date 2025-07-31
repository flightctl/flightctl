package tpm

import (
	"fmt"
)

// ownership handles TPM ownership workflow logic and password management
type ownership struct {
	client      *Client
	persistence *persistence
}

// newOwnership creates a new ownership instance with references to client and persistence
func newOwnership(client *Client, persistence *persistence) *ownership {
	return &ownership{
		client:      client,
		persistence: persistence,
	}
}

// ensureStorageHierarchyPassword ensures the storage hierarchy has a password set.
// If no password is currently set, it generates and sets a new random password.
// Returns the password being used for the storage hierarchy.
func (o *ownership) ensureStorageHierarchyPassword() ([]byte, error) {
	if o.client.storageHierarchyAuth != nil {
		return o.client.storageHierarchyAuth, nil
	}

	authSet, err := o.client.checkStorageHierarchyAuthStatus()
	if err != nil {
		return nil, fmt.Errorf("checking storage hierarchy auth status: %w", err)
	}

	if !authSet {
		password, err := o.client.generateStoragePassword()
		if err != nil {
			return nil, fmt.Errorf("generating storage hierarchy password: %w", err)
		}

		// ensure we persist the password before we change it
		err = o.persistence.savePassword(password)
		if err != nil {
			return nil, fmt.Errorf("writing storage password: %w", err)
		}

		if err := o.client.changeStorageHierarchyPassword(nil, password); err != nil {
			err = fmt.Errorf("setting storage hierarchy password: %w", err)
			if clearErr := o.persistence.clearPassword(); clearErr != nil {
				err = fmt.Errorf("%w clearing persisted password: %w", err, clearErr)
			}
			return nil, err
		}

		return password, nil
	}

	password, err := o.persistence.loadPassword()
	if err != nil {
		return nil, fmt.Errorf("reading storage password: %w", err)
	}
	return password, nil
}

// resetStorageHierarchyPassword resets the storage hierarchy password to empty.
// If no auth is currently set, it returns early without error.
// If auth is set, it unseals the current password and changes it back to empty.
func (o *ownership) resetStorageHierarchyPassword() error {
	if o.client.storageHierarchyAuth == nil {
		return nil
	}

	currentPassword, err := o.persistence.loadPassword()
	if err != nil {
		return fmt.Errorf("reading current storage password: %w", err)
	}

	if err := o.client.changeStorageHierarchyPassword(currentPassword, []byte{}); err != nil {
		return fmt.Errorf("changing storage hierarchy password to empty: %w", err)
	}

	if err := o.persistence.clearPassword(); err != nil {
		return fmt.Errorf("clearing storage password: %w", err)
	}

	return nil
}
