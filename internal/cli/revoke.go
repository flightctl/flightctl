package cli

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const maxRevocationReasonCharacters = 1024

type RevokeOptions struct {
	GlobalOptions
	Reason string
}

func DefaultRevokeOptions() *RevokeOptions {
	return &RevokeOptions{
		GlobalOptions: DefaultGlobalOptions(),
	}
}

func NewCmdRevoke() *cobra.Command {
	o := DefaultRevokeOptions()
	cmd := &cobra.Command{
		Use:   "revoke (TYPE/NAME | TYPE NAME)",
		Short: "Revoke an issued enrollment credential.",
		Long: `Revoke an issued enrollment credential.

Revocation blocks new enrollment with the credential, denies pending Enrollment Requests
bound to it, and flags devices admitted with it. It does not evict devices or stop
management certificate renewal.`,
		Example: `  # Revoke an enrollment credential CSR
  flightctl revoke csr/my-enrollment-credential --reason "device compromised"

  # Use the type and name as separate arguments
  flightctl revoke certificatesigningrequest my-enrollment-credential --reason "device compromised"`,
		Args: cobra.RangeArgs(1, 2),
		ValidArgsFunction: KindNameAutocomplete{
			Options:            o,
			AllowMultipleNames: false,
			AllowedKinds:       []ResourceKind{CertificateSigningRequestKind},
		}.ValidArgsFunction,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd, args); err != nil {
				return err
			}
			if err := o.Validate(args); err != nil {
				return err
			}
			ctx, cancel := o.WithTimeout(cmd.Context())
			defer cancel()
			return o.Run(ctx, args)
		},
		SilenceUsage: true,
	}
	o.Bind(cmd.Flags())
	// MarkFlagRequired only fails when the flag is undefined; "reason" is bound above.
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

func (o *RevokeOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
	fs.StringVarP(&o.Reason, "reason", "r", o.Reason, "Security reason for revoking the enrollment credential.")
}

func (o *RevokeOptions) Complete(cmd *cobra.Command, args []string) error {
	return o.GlobalOptions.Complete(cmd, args)
}

func (o *RevokeOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}

	kind, name, err := parseAndValidateKindNameFromArgsSingle(args)
	if err != nil {
		return err
	}

	if kind != CertificateSigningRequestKind {
		return fmt.Errorf("kind must be %s", CertificateSigningRequestKind)
	}

	if len(name) == 0 {
		return fmt.Errorf("specify a specific certificate signing request to revoke")
	}

	reason := strings.TrimSpace(o.Reason)
	if len(reason) == 0 {
		return fmt.Errorf("--reason is required to revoke a certificate signing request")
	}
	if utf8.RuneCountInString(reason) > maxRevocationReasonCharacters {
		return fmt.Errorf("--reason must be at most %d characters", maxRevocationReasonCharacters)
	}

	return nil
}

func (o *RevokeOptions) Run(ctx context.Context, args []string) error {
	c, err := o.BuildClient()
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}
	if err := c.Start(ctx); err != nil {
		return err
	}
	defer c.Stop()

	_, name, err := parseAndValidateKindNameFromArgsSingle(args)
	if err != nil {
		return err
	}

	return o.revoke(ctx, c.ClientWithResponses, name)
}

func (o *RevokeOptions) revoke(ctx context.Context, c *apiclient.ClientWithResponses, name string) error {
	response, err := c.RevokeCertificateSigningRequestWithResponse(ctx, name, api.EnrollmentCertificateRevocationRequest{
		Reason: o.Reason,
	})
	if err != nil {
		return fmt.Errorf("revoking certificate signing request %s: %w", name, err)
	}

	if response.StatusCode() != http.StatusOK {
		return &CLIError{
			Context: fmt.Sprintf("revoking certificate signing request %s: failed", name),
			Err:     &APIError{Status: ParseStatusFromBody(response.Body)},
		}
	}

	fmt.Printf("Revoked certificate signing request %q\n", name)
	return nil
}
