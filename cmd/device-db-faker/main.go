package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const (
	appName = "device-db-faker"
)

func main() {
	command := NewDBFakeCommand()
	if err := command.Execute(); err != nil {
		os.Exit(1)
	}
}

func NewDBFakeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   fmt.Sprintf("%s [flags] [options]", appName),
		Short: fmt.Sprintf("%s directly modifies devices in the DB for testing purposes.", appName),
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
			os.Exit(1)
		},
	}
	cmd.AddCommand(NewCmdUpdateStatus())

	return cmd
}
