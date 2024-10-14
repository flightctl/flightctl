package cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type CSRConfigOptions struct {
	GlobalOptions
	CertSigningRequestFile string
	Output                 string
	Name                   string
	ExpirationSeconds      string
	Overwrite              bool
}

func DefaultCSRConfigOptions() *CSRConfigOptions {
	return &CSRConfigOptions{
		GlobalOptions: DefaultGlobalOptions(),
		Overwrite:     false,
	}
}

func NewCmdCSRConfig() *cobra.Command {
	o := DefaultCSRConfigOptions()
	cmd := &cobra.Command{
		Use:   "csr-generate",
		Short: "Generate a CSR resource config .yaml based on a CSR file .csr",
		Args:  cobra.ExactArgs(1),
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
	_ = cmd.MarkFlagRequired("output")
	return cmd
}

func (o *CSRConfigOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
	fs.StringVarP(&o.Output, "output", "o", "", "Specify a filename in which to save the generated csr resource config")
	fs.StringVarP(&o.Name, "name", "n", "mycsr", "Specify a name for the csr")
	fs.StringVarP(&o.ExpirationSeconds, "expiry", "e", "604800", "Specify desired certificate expiration in seconds")
	fs.BoolVarP(&o.Overwrite, "overwrite", "y", false, "Setting this flag overwrites the specified output file without prompting the user")
}

func (o *CSRConfigOptions) Complete(cmd *cobra.Command, args []string) error {
	return o.GlobalOptions.Complete(cmd, args)
}

func (o *CSRConfigOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}

	if len(args) == 0 {
		return fmt.Errorf("must specify a CSR file")
	}
	_, err := os.ReadFile(args[0])
	if err != nil {
		return fmt.Errorf("invalid CSR file specified: %w", err)
	}

	return nil
}

func (o *CSRConfigOptions) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("must specify a CSR file")
	}

	data, err := os.ReadFile(args[0])
	if err != nil {
		return fmt.Errorf("invalid CSR file specified: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(data)

	output := fmt.Sprintf(`apiVersion: v1alpha1
kind: CertificateSigningRequest
metadata:
  name: %s
spec:
  request: %s
  signerName: ca
  usages: ["clientAuth", "CA:false"]
  expirationSeconds: %s
`, o.Name, encoded, o.ExpirationSeconds)

	if !o.Overwrite {
		_, err := os.Stat(o.Output)
		if err == nil {
			return fmt.Errorf("file already exists and overwrite is not set")
		}
	}

	f, err := os.Create(o.Output)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(output)
	if err != nil {
		return err
	}

	fmt.Printf("config file written to: %s\n", o.Output)

	return nil
}
