package main

import (
	"context"
	"fmt"
	"os"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

type UpdateStatusOptions struct {
	Filename      string
	LabelSelector string
}

func DefaultUpdateStatusOptions() *UpdateStatusOptions {
	return &UpdateStatusOptions{
		Filename:      "",
		LabelSelector: "",
	}
}

func NewCmdUpdateStatus() *cobra.Command {
	o := DefaultUpdateStatusOptions()
	cmd := &cobra.Command{
		Use:   "updatestatus -c COUNT -f FILENAME",
		Short: "Update device statuses in the DB.",
		RunE: func(cmd *cobra.Command, args []string) error {
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

func (o *UpdateStatusOptions) Bind(fs *pflag.FlagSet) {
	fs.StringVarP(&o.Filename, "filename", "f", o.Filename, "The file containing the resources to apply (yaml or json format).")
	fs.StringVarP(&o.LabelSelector, "selector", "l", o.LabelSelector, "Selector (label query) to filter on, as a comma-separated list of key=value.")
}

func (o *UpdateStatusOptions) Validate(args []string) error {
	if len(o.Filename) == 0 {
		return fmt.Errorf("must specify -f FILENAME")
	}
	if len(args) > 0 {
		return fmt.Errorf("unexpected arguments: %v", args)
	}
	return nil
}

func (o *UpdateStatusOptions) Run(ctx context.Context, args []string) error {
	r, err := os.Open(o.Filename)
	if err != nil {
		return fmt.Errorf("the path %q cannot be opened: %w", o.Filename, err)
	}
	defer r.Close()

	device := api.Device{}
	decoder := yamlutil.NewYAMLOrJSONDecoder(r, 100)
	err = decoder.Decode(&device)
	if err != nil {
		return fmt.Errorf("failed reading device from file: %w", err)
	}
	status := device.Status
	if status == nil {
		status = &api.DeviceStatus{}
	}

	faker := NewFaker()
	defer faker.Close()
	return faker.UpdateStatuses(ctx, status, o.LabelSelector)
}
