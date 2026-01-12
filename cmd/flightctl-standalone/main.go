package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	command := NewStandaloneCommand()
	if err := command.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func NewStandaloneCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flightctl-standalone [command]",
		Short: "Flight Control standalone utilities",
		Long:  "A collection of utilities for the Flight Control standalone deployment",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
			os.Exit(0)
		},
	}

	cmd.AddCommand(NewRenderCommand())
	cmd.AddCommand(NewCleanupCommand())

	return cmd
}

func NewRenderCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "render [command]",
		Short: "Render templates and configuration files",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
			os.Exit(0)
		},
	}

	cmd.AddCommand(NewRenderQuadletsCommand())
	cmd.AddCommand(NewRenderTemplateCommand())

	return cmd
}
