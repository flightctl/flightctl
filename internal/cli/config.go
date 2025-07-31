package cli

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type ConfigOptions struct {
	GlobalOptions
}

func DefaultConfigOptions() *ConfigOptions {
	return &ConfigOptions{
		GlobalOptions: DefaultGlobalOptions(),
	}
}

func NewCmdConfig() *cobra.Command {
	o := DefaultConfigOptions()
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage CLI configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())

	cmd.AddCommand(NewCmdConfigCurrentOrganization())
	cmd.AddCommand(NewCmdConfigSetOrganization())

	return cmd
}

func (o *ConfigOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
}

// NewCmdConfigCurrentOrganization creates a command to display the current organization
func NewCmdConfigCurrentOrganization() *cobra.Command {
	o := DefaultConfigOptions()
	cmd := &cobra.Command{
		Use:   "current-organization",
		Short: "Display the current organization",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			if err := o.ValidateCmd(args); err != nil {
				return err
			}
			return o.RunCurrentOrganization(cmd.Context(), args)
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())
	return cmd
}

// NewCmdConfigSetOrganization creates a command to set the current organization
func NewCmdConfigSetOrganization() *cobra.Command {
	o := DefaultConfigOptions()
	cmd := &cobra.Command{
		Use:   "set-organization <organization-id>",
		Short: "Set the current organization (empty string to unset)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			if err := o.ValidateCmd(args); err != nil {
				return err
			}
			return o.RunSetOrganization(cmd.Context(), args)
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())
	return cmd
}

func (o *ConfigOptions) Complete(cmd *cobra.Command, args []string) error {
	return o.GlobalOptions.Complete(cmd, args)
}

func (o *ConfigOptions) RunCurrentOrganization(ctx context.Context, args []string) error {
	config, err := client.ParseConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}

	if config.Organization == "" {
		fmt.Println("No current organization set")
	} else {
		fmt.Println(config.Organization)
	}
	return nil
}

func (o *ConfigOptions) RunSetOrganization(ctx context.Context, args []string) error {
	organizationId := args[0]

	// Validate organization ID unless it's empty (empty string unsets the organization)
	if organizationId != "" {
		if err := validateOrganizationID(organizationId); err != nil {
			return fmt.Errorf("cannot set organization: %w", err)
		}
	}

	config, err := client.ParseConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	if config.Organization == organizationId {
		fmt.Println("Current organization unchanged")
		return nil
	}

	config.Organization = organizationId

	if err := config.Persist(o.ConfigFilePath); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	if organizationId == "" {
		fmt.Println("Current organization unset")
	} else {
		fmt.Printf("Current organization set to: %s\n", organizationId)
	}
	return nil
}
