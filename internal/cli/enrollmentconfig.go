package cli

import (
	"context"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"sigs.k8s.io/yaml"
)

type EnrollmentConfigOptions struct {
	GlobalOptions
}

func DefaultEnrollmentConfigOptions() *EnrollmentConfigOptions {
	return &EnrollmentConfigOptions{
		GlobalOptions: DefaultGlobalOptions(),
	}
}

func NewCmdEnrollmentConfig() *cobra.Command {
	o := DefaultEnrollmentConfigOptions()
	cmd := &cobra.Command{
		Use:   "enrollmentconfig",
		Short: "Get enrollment config for devices",
		Args:  cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			if err := o.Validate(args); err != nil {
				return err
			}
			return o.Run(cmd.Context(), args)
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())
	return cmd
}

func (o *EnrollmentConfigOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
}

func (o *EnrollmentConfigOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}
	return nil
}

func (o *EnrollmentConfigOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}
	return nil
}

func (o *EnrollmentConfigOptions) Run(ctx context.Context, args []string) error {
	c, err := client.NewFromConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	response, err := c.GetEnrollmentConfigWithResponse(ctx)
	if err != nil {
		return err
	}

	err = validateHttpResponse(response.Body, response.StatusCode(), http.StatusOK)
	if err != nil {
		return fmt.Errorf("failed to get enrollment config: %w", err)
	}

	marshalled, err := yaml.Marshal(response.JSON200)
	if err != nil {
		return fmt.Errorf("marshalling resource: %w", err)
	}
	fmt.Println(marshalled)
	return nil
}
