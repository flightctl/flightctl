package cli

import (
	"github.com/spf13/cobra"
)

// NewCmdApp creates the "app" parent command, grouping the lifecycle commands
// (start/stop/restart/console) for applications running on devices or fleets.
func NewCmdApp() *cobra.Command {
	o := DefaultGlobalOptions()
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Manage the lifecycle of applications running on devices or fleets.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())

	cmd.AddCommand(NewCmdAppStart())
	cmd.AddCommand(NewCmdAppStop())
	cmd.AddCommand(NewCmdAppRestart())
	cmd.AddCommand(NewCmdAppConsole())

	return cmd
}
