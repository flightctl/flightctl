package userutil

import (
	"os/user"
	"strconv"

	"github.com/flightctl/flightctl/api/core/v1beta1"
)

// LookupUser gets the uid, gid and homedir for the given user, in a format fit for Linux environments.
func LookupUser(username v1beta1.Username) (uid uint32, gid uint32, homeDir string, err error) {
	user, err := user.Lookup(username.String())
	if err != nil {
		return 0, 0, "", err
	}

	return accountDetails(user)
}

// LookupExecutionUser resolves an omitted username to the process account.
func LookupExecutionUser(username v1beta1.Username) (uid uint32, gid uint32, homeDir string, err error) {
	if !username.IsCurrentProcessUser() {
		return LookupUser(username)
	}
	account, err := user.Current()
	if err != nil {
		return 0, 0, "", err
	}
	return accountDetails(account)
}

func accountDetails(account *user.User) (uid uint32, gid uint32, homeDir string, err error) {
	intUid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return 0, 0, "", err
	}

	intGid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil {
		return 0, 0, "", err
	}

	return uint32(intUid), uint32(intGid), account.HomeDir, nil //nolint:gosec // IDs are parsed as uint32
}
