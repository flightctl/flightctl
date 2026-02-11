package client

import (
	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/userutil"
)

func ExecuterForUser(username v1beta1.Username) (executer.Executer, error) {
	var execOpts []executer.ExecuterOption

	if !username.IsCurrentProcessUser() {
		uid, gid, homeDir, err := userutil.LookupUser(username)
		if err != nil {
			return nil, err
		}
		execOpts = append(execOpts,
			executer.WithHomeDir(homeDir),
			executer.WithUIDAndGID(uid, gid),
		)
	}

	return executer.NewCommonExecuter(execOpts...), nil
}
