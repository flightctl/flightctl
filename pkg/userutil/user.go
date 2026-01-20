package userutil

import (
	"os/user"
	"strconv"
)

// LookupUser gets the uid, gid and homedir for the given user, in a format fit for Linux environments.
func LookupUser(username string) (uid uint32, gid uint32, homeDir string, err error) {
	user, err := user.Lookup(username)
	if err != nil {
		return 0, 0, "", err
	}

	intUid, err := strconv.Atoi(user.Uid)
	if err != nil {
		return 0, 0, "", err
	}

	intGid, err := strconv.Atoi(user.Gid)
	if err != nil {
		return 0, 0, "", err
	}

	return uint32(intUid), uint32(intGid), user.HomeDir, nil //nolint:gosec
}
