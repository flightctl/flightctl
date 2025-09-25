package cli

import (
	"context"
	"fmt"
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
		fmt.Println(config.Organization)
	}
	return nil
}

func (o *ConfigOptions) RunSetOrganization(ctx context.Context, args []string) error {
	organizationId := args[0]

	config, err := client.ParseConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	// Check if organization is unchanged first to avoid unnecessary API calls
	if config.Organization == organizationId {
		fmt.Println("Current organization unchanged")
		return nil
	}

	// Validate organization ID unless it's empty (empty string unsets the organization)
	if organizationId != "" {
		if err := validateOrganizationID(organizationId); err != nil {
			return fmt.Errorf("cannot set organization: %w", err)
		}
		// Validate that the organization exists and is accessible to the user
		if err := o.validateOrganizationExists(ctx, organizationId); err != nil {
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
		fmt.Printf("Current organization set to: %s\n", organizationId)
	}
	return nil
}

// validateOrganizationExists checks if the given organization ID exists and is accessible to the user.
func (o *ConfigOptions) validateOrganizationExists(ctx context.Context, organizationId string) error {
	// Empty organization ID is valid (unsets the organization).
	if organizationId == "" || organizationId == org.DefaultID.String() {
		return nil
	}

	// Build API client to fetch organizations.
	c, err := o.BuildClient()
	if err != nil {
		return fmt.Errorf("failed to create API client: %w", err)
	}

	// Fetch user's organizations.
	response, err := c.ListOrganizationsWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch organizations: %w", err)
	}

	// Validate the API response.
	if response.StatusCode() != 200 || response.JSON200 == nil || response.JSON200.Items == nil {
		return fmt.Errorf("failed to fetch organizations: invalid response (status: %d)", response.StatusCode())
	}

	orgs := make(map[string]string)
	for _, org := range response.JSON200.Items {
		if org.Metadata.Name != nil {
			displayName := ""
			if org.Spec != nil && org.Spec.DisplayName != nil {
				displayName = fmt.Sprintf(" (%s)", *org.Spec.DisplayName)
			}
			orgs[*org.Metadata.Name] = fmt.Sprintf("%s%s", *org.Metadata.Name, displayName)
		}
	}

	// Check if the organization ID exists.
	if _, ok := orgs[organizationId]; ok {
		return nil // Organization found and accessible.
	}

	// Organization not found - provide helpful error message.
	availableOrgs := make([]string, 0, len(orgs))
	for _, name := range orgs {
		availableOrgs = append(availableOrgs, name)
	}

	return fmt.Errorf("organization %q not found - available organizations: %s", organizationId, strings.Join(availableOrgs, ", "))
}
