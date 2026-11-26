package cli

import (
	"context"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/spf13/cobra"
)

func NewCmdOverride() *cobra.Command {
	options := DefaultGlobalOptions()
	var reason string
	cmd := &cobra.Command{
		Use:   "override (device/NAME | device NAME)",
		Short: "Dismiss a device's revoked enrollment credential flag.",
		Long: `Dismiss a device's revoked enrollment credential flag. The server records the
administrator, time, and reason for the dismissal. The revoked credential still
blocks new enrollment.`,
		Example: `  flightctl override device/my-device --reason "Reviewed device enrollment"`,
		Args:    cobra.RangeArgs(1, 2),
		ValidArgsFunction: KindNameAutocomplete{
			Options:      &options,
			AllowedKinds: []ResourceKind{DeviceKind},
		}.ValidArgsFunction,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := options.Complete(cmd, args); err != nil {
				return err
			}
			if err := options.Validate(args); err != nil {
				return err
			}
			kind, name, err := parseAndValidateKindNameFromArgsSingle(args)
			if err != nil {
				return err
			}
			if kind != DeviceKind {
				return fmt.Errorf("kind must be %s", DeviceKind)
			}
			auditReason, err := validation.NormalizeEnrollmentCredentialAuditReason(reason)
			if err != nil {
				return fmt.Errorf("invalid --reason: %w", err)
			}
			ctx, cancel := options.WithTimeout(cmd.Context())
			defer cancel()
			if err := overrideDeviceCredentialFlag(ctx, &options, name, auditReason); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Dismissed revoked enrollment credential flag on device %q\n", name)
			return err
		},
		SilenceUsage: true,
	}
	options.Bind(cmd.Flags())
	cmd.Flags().StringVarP(&reason, "reason", "r", "", "Audit reason for dismissing the flag.")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

func overrideDeviceCredentialFlag(ctx context.Context, options *GlobalOptions, name, reason string) error {
	c, err := options.BuildClient()
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}
	if err := c.Start(ctx); err != nil {
		return err
	}
	defer c.Stop()

	operation := fmt.Sprintf("dismissing revoked enrollment credential flag on device %q", name)
	response, err := c.OverrideDeviceEnrollmentCredentialWithResponse(ctx, name, api.DeviceEnrollmentCredentialOverrideRequest{
		Reason: reason,
	})
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if response.StatusCode() != http.StatusOK {
		return &CLIError{Context: operation, Err: &APIError{Status: ParseStatusFromBody(response.Body)}}
	}
	return nil
}
