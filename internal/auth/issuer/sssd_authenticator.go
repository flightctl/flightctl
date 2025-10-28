//go:build linux

package issuer

import (
	"fmt"
	"os/user"
	"strconv"

	"github.com/godbus/dbus/v5"
)

// RealSSSDAuthenticator implements SSSD authentication using D-Bus
type RealSSSDAuthenticator struct {
	conn *dbus.Conn
}

// NewRealSSSDAuthenticator creates a new SSSD authenticator
func NewRealSSSDAuthenticator() (*RealSSSDAuthenticator, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to system D-Bus: %w", err)
	}

	return &RealSSSDAuthenticator{
		conn: conn,
	}, nil
}

// Authenticate performs SSSD authentication using D-Bus
func (r *RealSSSDAuthenticator) Authenticate(username, password string) error {
	// Get the SSSD service object
	sssdObj := r.conn.Object("org.freedesktop.sssd.infopipe", "/org/freedesktop/sssd/infopipe")

	// Call the Authenticate method
	var result bool
	err := sssdObj.Call("org.freedesktop.sssd.infopipe.Authenticate", 0, username, password).Store(&result)
	if err != nil {
		return fmt.Errorf("SSSD authentication failed: %w", err)
	}

	if !result {
		return fmt.Errorf("authentication failed for user %s", username)
	}

	return nil
}

// LookupUser looks up a user by username using SSSD via D-Bus
func (r *RealSSSDAuthenticator) LookupUser(username string) (*user.User, error) {
	// Get the SSSD service object
	sssdObj := r.conn.Object("org.freedesktop.sssd.infopipe", "/org/freedesktop/sssd/infopipe")

	// Call the GetUserAttr method to get user attributes
	var attrs map[string][]string
	err := sssdObj.Call("org.freedesktop.sssd.infopipe.GetUserAttr", 0, username, []string{"uidNumber", "gidNumber", "gecos", "homeDirectory", "loginShell"}).Store(&attrs)
	if err != nil {
		// Fallback to system user lookup if SSSD fails
		return user.Lookup(username)
	}

	// Extract user information from SSSD attributes
	uidStr := getFirstValue(attrs, "uidNumber")
	gidStr := getFirstValue(attrs, "gidNumber")
	gecos := getFirstValue(attrs, "gecos")
	homeDir := getFirstValue(attrs, "homeDirectory")
	shell := getFirstValue(attrs, "loginShell")

	// Parse UID and GID to validate they are valid integers
	_, err = strconv.Atoi(uidStr)
	if err != nil {
		return nil, fmt.Errorf("invalid UID from SSSD: %s", uidStr)
	}

	_, err = strconv.Atoi(gidStr)
	if err != nil {
		return nil, fmt.Errorf("invalid GID from SSSD: %s", gidStr)
	}

	// Create user.User struct
	u := &user.User{
		Uid:      uidStr,
		Gid:      gidStr,
		Username: username,
		Name:     gecos,
		HomeDir:  homeDir,
	}

	// Set shell if available
	if shell != "" {
		// Note: user.User doesn't have a Shell field, but we can store it in Name if needed
		// For now, we'll just use the gecos as the name
	}

	return u, nil
}

// GetUserGroups gets the groups for a user using SSSD via D-Bus
func (r *RealSSSDAuthenticator) GetUserGroups(systemUser *user.User) ([]string, error) {
	// Get the SSSD service object
	sssdObj := r.conn.Object("org.freedesktop.sssd.infopipe", "/org/freedesktop/sssd/infopipe")

	// Call the GetUserGroups method
	var groups []string
	err := sssdObj.Call("org.freedesktop.sssd.infopipe.GetUserGroups", 0, systemUser.Username).Store(&groups)
	if err != nil {
		// Fallback to system group lookup if SSSD fails
		return r.getSystemUserGroups(systemUser)
	}

	return groups, nil
}

// getSystemUserGroups is a fallback method to get groups from the system
func (r *RealSSSDAuthenticator) getSystemUserGroups(systemUser *user.User) ([]string, error) {
	// Get group IDs for the user
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

// getFirstValue gets the first value from a map of string slices
func getFirstValue(attrs map[string][]string, key string) string {
	if values, exists := attrs[key]; exists && len(values) > 0 {
		return values[0]
	}
	return ""
}

// Close closes the D-Bus connection
func (r *RealSSSDAuthenticator) Close() error {
	if r.conn != nil {
		return r.conn.Close()
	}
	return nil
}
