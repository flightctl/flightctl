//go:build linux

package pam

import (
	"fmt"
	"os/user"

	"github.com/msteinert/pam/v2"
)

// RealPAMAuthenticator implements Linux authentication using PAM and NSS
// PAM (Pluggable Authentication Modules) handles authentication
// NSS (Name Service Switch) handles user/group lookups via user.Lookup()
// Works with any system-configured authentication backend
type RealPAMAuthenticator struct {
	pamService string
}

// NewRealPAMAuthenticator creates a new Linux authenticator
// Uses PAM for authentication and NSS for user/group information
// Automatically works with any configured Linux authentication backend
func NewRealPAMAuthenticator() (*RealPAMAuthenticator, error) {
	return &RealPAMAuthenticator{
		pamService: "flightctl", // Service-specific PAM config (includes system-auth)
	}, nil
}

// Authenticate performs authentication using PAM
// PAM automatically uses the system-configured authentication backend
func (r *RealPAMAuthenticator) Authenticate(username, password string) error {
	fmt.Printf("PAM Authenticate: starting PAM authentication for user %s with service %s, password length=%d\n", username, r.pamService, len(password))

	// Start PAM transaction
	t, err := pam.StartFunc(r.pamService, username, func(s pam.Style, msg string) (string, error) {
		fmt.Printf("PAM Authenticate: received PAM message - style=%v, msg=%s\n", s, msg)
		switch s {
		case pam.PromptEchoOff:
			// Password prompt (hidden input)
			fmt.Printf("PAM Authenticate: responding to PromptEchoOff (password prompt)\n")
			return password, nil
		case pam.PromptEchoOn:
			// Username or other visible prompt
			fmt.Printf("PAM Authenticate: responding to PromptEchoOn (username prompt)\n")
			return "", nil
		case pam.ErrorMsg:
			fmt.Printf("PAM Authenticate: received ErrorMsg - %s\n", msg)
			return "", fmt.Errorf("PAM error: %s", msg)
		case pam.TextInfo:
			// Informational message, no response needed
			fmt.Printf("PAM Authenticate: received TextInfo - %s\n", msg)
			return "", nil
		default:
			fmt.Printf("PAM Authenticate: unrecognized message style - %v\n", s)
			return "", fmt.Errorf("unrecognized PAM message style: %v", s)
		}
	})
	if err != nil {
		fmt.Printf("PAM Authenticate: failed to start PAM transaction - %v\n", err)
		return fmt.Errorf("failed to start PAM transaction: %w", err)
	}
	defer t.End()

	fmt.Printf("PAM Authenticate: PAM transaction started, calling Authenticate()\n")
	// Authenticate the user
	if err := t.Authenticate(0); err != nil {
		fmt.Printf("PAM Authenticate: authentication failed - %v\n", err)
		return fmt.Errorf("authentication failed: %w", err)
	}

	fmt.Printf("PAM Authenticate: authentication successful for user %s\n", username)
	return nil
}

// LookupUser looks up a user by username using NSS
// NSS (Name Service Switch) automatically uses the appropriate backend
func (r *RealPAMAuthenticator) LookupUser(username string) (*user.User, error) {
	return user.Lookup(username)
}

// GetUserGroups gets the groups for a user using NSS
// NSS (Name Service Switch) automatically uses the appropriate backend
func (r *RealPAMAuthenticator) GetUserGroups(systemUser *user.User) ([]string, error) {
	// Get group IDs for the user via NSS
	groupIds, err := systemUser.GroupIds()
	if err != nil {
		return nil, fmt.Errorf("failed to get group IDs: %w", err)
	}

	// Convert group IDs to group names
	var groupNames []string
	for _, gid := range groupIds {
		group, err := user.LookupGroupId(gid)
		if err != nil {
			// Skip groups that can't be looked up
			continue
		}
		groupNames = append(groupNames, group.Name)
	}

	return groupNames, nil
}

// Close is a no-op since we don't hold any resources
func (r *RealPAMAuthenticator) Close() error {
	return nil
}
