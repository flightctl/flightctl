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
		Short: "flightctl controls the Flight Control fleet management service.",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
			os.Exit(1)
		},
	}
	cmd.AddCommand(cli.NewCmdGet())
	cmd.AddCommand(cli.NewCmdApply())
	cmd.AddCommand(cli.NewCmdDelete())
	cmd.AddCommand(cli.NewCmdApprove())
	cmd.AddCommand(cli.NewCmdCSRConfig())
	cmd.AddCommand(cli.NewCmdConfig())
	cmd.AddCommand(cli.NewCmdDecommission())
	cmd.AddCommand(cli.NewCmdDeny())
	cmd.AddCommand(cli.NewCmdLogin())
	cmd.AddCommand(cli.NewCmdResume())
	cmd.AddCommand(cli.NewCmdVersion())
	cmd.AddCommand(cli.NewConsoleCmd())
	cmd.AddCommand(cli.NewCmdCompletion())
	cmd.AddCommand(cli.NewCmdEnrollmentConfig())
	cmd.AddCommand(cli.NewCmdCertificate())

	return cmd
}
