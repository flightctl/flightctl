package userutil

import (
	"os/user"
	"strconv"

	"github.com/flightctl/flightctl/api/core/v1beta1"
)

// Account contains the numeric IDs and home directory of a local user.
type Account struct {
	UID, GID uint32
	HomeDir  string
}

// LookupUser gets the uid, gid and homedir for the given user, in a format fit for Linux environments.
func LookupUser(username v1beta1.Username) (uid uint32, gid uint32, homeDir string, err error) {
	account, err := lookupAccount(username)
	return account.UID, account.GID, account.HomeDir, err
}

// LookupExecutionUser resolves an omitted username to the process account.
func LookupExecutionUser(username v1beta1.Username) (Account, error) {
	if !username.IsCurrentProcessUser() {
		return lookupAccount(username)
	}
	account, err := user.Current()
	if err != nil {
		return Account{}, err
	}
	return accountDetails(account)
}

func lookupAccount(username v1beta1.Username) (Account, error) {
	account, err := user.Lookup(username.String())
	if err != nil {
		return Account{}, err
	}
	return accountDetails(account)
}

func accountDetails(account *user.User) (Account, error) {
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return Account{}, err
	}

	gid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil {
		return Account{}, err
	}

	return Account{UID: uint32(uid), GID: uint32(gid), HomeDir: account.HomeDir}, nil //nolint:gosec // IDs are parsed as uint32
}
