package cli

import (
	"context"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type ResumeOptions struct {
	GlobalOptions
	LabelSelector string
	FieldSelector string
	All           bool
}

func DefaultResumeOptions() *ResumeOptions {
	return &ResumeOptions{
		GlobalOptions: DefaultGlobalOptions(),
		LabelSelector: "",
		FieldSelector: "",
		All:           false,
	}
}

func NewCmdResume() *cobra.Command {
	o := DefaultResumeOptions()
	cmd := &cobra.Command{
		Use:   "resume (device/NAME | device | devices)",
		Short: "Resume a device or devices based on selectors or all devices.",
		Long: `Resume devices that are in a conflictPaused state.

Examples:
  # Resume a specific device
  flightctl resume device/my-device

  # Resume all devices in conflictPaused state
  flightctl resume devices --all

  # Resume all devices with a specific label (using plural form)
  flightctl resume devices --selector env=production

  # Resume all devices with a specific label (using singular form)
  flightctl resume device --selector env=production

  # Resume devices with multiple label conditions
  flightctl resume devices --selector env=staging,tier!=api

  # Resume devices with a field selector
  flightctl resume devices --field-selector metadata.name=my-device

  # Resume devices with both label and field selectors
  flightctl resume devices --selector env=production --field-selector status.phase!=Pending`,
		Args: cobra.ExactArgs(1),
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

func (o *ResumeOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
	fs.StringVarP(&o.LabelSelector, "selector", "l", o.LabelSelector, "Selector (label query) to filter on, supporting operators like '=', '!=', and 'in' (e.g., -l='key1=value1,key2!=value2,key3 in (value3, value4)').")
	fs.StringVar(&o.FieldSelector, "field-selector", o.FieldSelector, "Selector (field query) to filter on, supporting operators like '=', '==', and '!=' (e.g., --field-selector='key1=value1,key2!=value2').")
	fs.BoolVar(&o.All, "all", o.All, "Resume all devices in conflictPaused state.")
}

func (o *ResumeOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}
	return nil
}

func (o *ResumeOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}

	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}

	if kind != DeviceKind {
		return fmt.Errorf("kind must be Device")
	}

	// Handle bulk resume case (name is empty for plural forms)
	if name == "" {
		// Check for mutually exclusive flags
		if o.All && (o.LabelSelector != "" || o.FieldSelector != "") {
			return fmt.Errorf("--all flag cannot be used with selectors")
		}

		// Require at least one selector or --all flag
		if !o.All && o.LabelSelector == "" && o.FieldSelector == "" {
			return fmt.Errorf("at least one selector or --all flag is required when resuming multiple devices. Use --selector/-l, --field-selector, or --all flag")
		}
		return nil
	}

	// Handle single device case (name is provided)
	if o.All {
		return fmt.Errorf("--all flag cannot be used when resuming a specific device")
	}
	if o.LabelSelector != "" {
		return fmt.Errorf("label selector cannot be used when resuming a specific device")
	}
	if o.FieldSelector != "" {
		return fmt.Errorf("field selector cannot be used when resuming a specific device")
	}

	return nil
}

func (o *ResumeOptions) Run(ctx context.Context, args []string) error {
	c, err := o.BuildClient()
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	_, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}

	// Handle bulk resume case (name is empty for plural forms)
	if name == "" {
		return o.runBulkResume(ctx, c)
	}

	// Handle single device case (name is provided)
	return o.runSingleResume(ctx, c, name)
}

func (o *ResumeOptions) runSingleResume(ctx context.Context, c *apiclient.ClientWithResponses, name string) error {
	// Use the bulk resume API with a field selector to target the specific device
	fieldSelector := fmt.Sprintf("metadata.name=%s", name)
	request := api.DeviceResumeRequest{
		FieldSelector: &fieldSelector,
	}

	response, err := c.ResumeDevicesWithResponse(ctx, request)
	if err != nil {
		return fmt.Errorf("resuming device %s: %w", name, err)
	}

	if response.HTTPResponse != nil {
		switch response.HTTPResponse.StatusCode {
		case http.StatusOK:
			if response.JSON200.ResumedDevices == 1 {
				fmt.Printf("Resume request for %s \"%s\" completed\n", DeviceKind, name)
			} else {
				fmt.Printf("failed resuming device %s, device doesnt exists or already resumed\n", name)
			}
		case http.StatusBadRequest:
			return fmt.Errorf("invalid request for device %s", name)
		default:
			return fmt.Errorf("unsuccessful resume request for device %s: %s", name, string(response.Body))
		}
	}

	return nil
}

func (o *ResumeOptions) runBulkResume(ctx context.Context, c *apiclient.ClientWithResponses) error {
	request := api.DeviceResumeRequest{}

	// Build selector description for error messages
	var selectorDesc string

	if o.All {
		selectorDesc = "all devices"
		// When --all is specified, use a field selector that matches all devices
		allDevicesSelector := "metadata.name!=''"
		request.FieldSelector = &allDevicesSelector
	} else {
		if o.LabelSelector != "" {
			request.LabelSelector = &o.LabelSelector
		}
		if o.FieldSelector != "" {
			request.FieldSelector = &o.FieldSelector
		}

		if o.LabelSelector != "" && o.FieldSelector != "" {
			selectorDesc = fmt.Sprintf("label selector '%s' and field selector '%s'", o.LabelSelector, o.FieldSelector)
		} else if o.LabelSelector != "" {
			selectorDesc = fmt.Sprintf("label selector '%s'", o.LabelSelector)
		} else {
			selectorDesc = fmt.Sprintf("field selector '%s'", o.FieldSelector)
		}
	}

	response, err := c.ResumeDevicesWithResponse(ctx, request)
	if err != nil {
		return fmt.Errorf("resuming devices with %s: %w", selectorDesc, err)
	}

	if response.HTTPResponse != nil {
		switch response.HTTPResponse.StatusCode {
		case http.StatusOK:
			if response.JSON200 != nil {
				count := response.JSON200.ResumedDevices
				fmt.Printf("Resume operation completed:\n")
				fmt.Printf("  Devices resumed: %d\n", count)

				if count == 0 {
					fmt.Printf("No devices matched the selector or were in conflictPaused state\n")
				}
			}
		case http.StatusBadRequest:
			return fmt.Errorf("invalid selector: %s", selectorDesc)
		default:
			return fmt.Errorf("unsuccessful bulk resume request: %s", string(response.Body))
		}
	}

	return nil
}
