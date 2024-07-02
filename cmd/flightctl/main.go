package main

import (
	"os"

	"github.com/flightctl/flightctl/internal/cli"
	"github.com/spf13/cobra"
)

func main() {
	command := NewFlightCtlCommand()
	if err := command.Execute(); err != nil {
		os.Exit(1)
	}
}

func NewFlightCtlCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flightctl [flags] [options]",
		Short: "flightctl controls the Flight Control device management service.",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
			os.Exit(1)
		},
	}
	cmd.AddCommand(cli.NewCmdGet())
	cmd.AddCommand(cli.NewCmdApply())
	cmd.AddCommand(cli.NewCmdDelete())
	cmd.AddCommand(cli.NewCmdApprove())
	cmd.AddCommand(cli.NewCmdLogin())
	return cmd
}
