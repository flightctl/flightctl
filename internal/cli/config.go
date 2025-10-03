package cli

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/org"
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
		displayName, err := o.getOrganizationDisplayName(ctx, config.Organization)
		if err != nil || displayName == "" {
			fmt.Println(config.Organization)
		} else {
			fmt.Printf("%s %s\n", config.Organization, displayName)
		}
	}
	return nil
}

func (o *ConfigOptions) RunSetOrganization(ctx context.Context, args []string) error {
	organizationId := args[0]

	config, err := client.ParseConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	if config.Organization == organizationId {
		if organizationId == "" {
			fmt.Println("Current organization already unset")
		} else {
			if displayName, err := o.getOrganizationDisplayName(ctx, organizationId); err == nil && displayName != "" {
				fmt.Printf("Current organization unchanged: %s %s\n", organizationId, displayName)
			} else {
				fmt.Printf("Current organization unchanged: %s\n", organizationId)
			}
		}
		return nil
	}

	var displayName string
	if organizationId != "" {
		if err := validateOrganizationID(organizationId); err != nil {
			return fmt.Errorf("cannot set organization: %w", err)
		}
		displayName, err = o.getOrganizationDisplayName(ctx, organizationId)
		if err != nil {
			return fmt.Errorf("cannot set organization: %w", err)
		}
	}

	config.Organization = organizationId

	if err := config.Persist(o.ConfigFilePath); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	if organizationId == "" {
		fmt.Println("Current organization unset")
	} else {
		if displayName != "" {
			fmt.Printf("Current organization set to: %s %s\n", organizationId, displayName)
		} else {
			fmt.Printf("Current organization set to: %s\n", organizationId)
		}
	}
	return nil
}

func (o *ConfigOptions) getOrganizationDisplayName(ctx context.Context, organizationId string) (string, error) {
	if organizationId == "" || organizationId == org.DefaultID.String() {
		return "", nil
	}

	c, err := client.NewFromConfigFile(o.ConfigFilePath, client.WithOrganization(""))
	if err != nil {
		return "", fmt.Errorf("failed to create API client: %w", err)
	}

	response, err := c.ListOrganizationsWithResponse(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to fetch organizations: %w", err)
	}

	if response.StatusCode() != http.StatusOK || response.JSON200 == nil || response.JSON200.Items == nil {
		return "", fmt.Errorf("failed to fetch organizations: invalid response (status: %d)", response.StatusCode())
	}

	availableOrgs := make([]string, 0, len(response.JSON200.Items))
	for _, item := range response.JSON200.Items {
		if item.Metadata.Name == nil {
			continue
		}
		name := *item.Metadata.Name
		displayName := ""
		if item.Spec != nil && item.Spec.DisplayName != nil {
			displayName = *item.Spec.DisplayName
		}
		if displayName != "" {
			availableOrgs = append(availableOrgs, fmt.Sprintf("%s (%s)", name, displayName))
		} else {
			availableOrgs = append(availableOrgs, name)
		}
		if name != organizationId {
			continue
		}
		return displayName, nil
	}
	return "", fmt.Errorf("organization %q not found - available organizations: %s", organizationId, strings.Join(availableOrgs, ", "))
}
