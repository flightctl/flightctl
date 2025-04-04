package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type ApproveOptions struct {
	GlobalOptions

	ApproveLabels []string
}

func DefaultApproveOptions() *ApproveOptions {
	return &ApproveOptions{
		GlobalOptions: DefaultGlobalOptions(),
		ApproveLabels: []string{},
	}
}

func NewCmdApprove() *cobra.Command {
	o := DefaultApproveOptions()
	cmd := &cobra.Command{
		Use:   "approve TYPE/NAME",
		Short: "Approve a certificate signing or enrollment request.",
		Args:  cobra.ExactArgs(1),
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
	return cmd
}

func (o *ApproveOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)

	fs.StringArrayVarP(&o.ApproveLabels, "label", "l", []string{}, "Labels to add to the device, as a comma-separated list of key=value.")
}

func (o *ApproveOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}

	return nil
}

func (o *ApproveOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}

	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}

	if kind != EnrollmentRequestKind && kind != CertificateSigningRequestKind {
		return fmt.Errorf("kind must be either %s or %s", EnrollmentRequestKind, CertificateSigningRequestKind)
	}

	if len(name) == 0 {
		return fmt.Errorf("specify a specific request resource to approve")
	}

	if len(o.ApproveLabels) > 0 && kind != EnrollmentRequestKind {
		return fmt.Errorf("labels only apply to %s approval", EnrollmentRequestKind)
	}

	return nil
}

func (o *ApproveOptions) Run(ctx context.Context, args []string) error {
	c, err := client.NewFromConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}

	var response *http.Response
	var getResponse *apiclient.GetCertificateSigningRequestResponse

	switch {
	case kind == EnrollmentRequestKind:
		labels := util.LabelArrayToMap(o.ApproveLabels)
		approval := api.EnrollmentRequestApproval{
			Approved: true,
			Labels:   &labels,
		}
		response, err = c.ApproveEnrollmentRequest(ctx, name, approval)
	case kind == CertificateSigningRequestKind:
		getResponse, err = c.GetCertificateSigningRequestWithResponse(ctx, name)
		if err != nil {
			return fmt.Errorf("getting certificate signing request: %w", err)
		}
		if getResponse.HTTPResponse != nil {
			defer getResponse.HTTPResponse.Body.Close()
		}
		if getResponse.StatusCode() != http.StatusOK {
			return fmt.Errorf("getting certificate signing request: %d", getResponse.StatusCode())
		}
		if getResponse.JSON200 == nil {
			return fmt.Errorf("getting certificate signing request: empty response")
		}
		csr := getResponse.JSON200

		api.SetStatusCondition(&csr.Status.Conditions, api.Condition{
			Type:    api.ConditionTypeCertificateSigningRequestApproved,
			Status:  api.ConditionStatusTrue,
			Reason:  "Approved",
			Message: "Approved",
		})
		api.RemoveStatusCondition(&csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestDenied)
		api.RemoveStatusCondition(&csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestFailed)
		response, err = c.UpdateCertificateSigningRequestApproval(ctx, name, *csr)
	default:
		return fmt.Errorf("unsupported resource kind: %s", kind)
	}

	return processApprovalReponse(response, err, kind, name)
}

func processApprovalReponse(response *http.Response, err error, kind string, name string) error {
	errorPrefix := fmt.Sprintf("approving %s/%s", kind, name)
	if err != nil {
		return fmt.Errorf("%s: %w", errorPrefix, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var responseError api.Status
		// not handling errors as we are only interested in the message
		// and in case there will be a problem in reading the body or unmarshalling it
		// we will print the status like
		// Error: approving enrollmentrequest/<name>:  (422 Unprocessable Entity)
		body, _ := io.ReadAll(response.Body)
		_ = json.Unmarshal(body, &responseError)

		return fmt.Errorf("%s: %s (%s)", errorPrefix, responseError.Message, response.Status)
	}

	return nil
}
