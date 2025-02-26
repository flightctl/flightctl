package cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/client"
	fccrypto "github.com/flightctl/flightctl/internal/crypto"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"sigs.k8s.io/yaml"
)

type EnrollmentConfigOptions struct {
	GlobalOptions

	PrivateKey string
}

func DefaultEnrollmentConfigOptions() *EnrollmentConfigOptions {
	return &EnrollmentConfigOptions{
		GlobalOptions: DefaultGlobalOptions(),
	}
}

func NewCmdEnrollmentConfig() *cobra.Command {
	o := DefaultEnrollmentConfigOptions()
	cmd := &cobra.Command{
		Use:   "enrollmentconfig [CSR_NAME]",
		Short: "Get enrollment config for devices",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return fmt.Errorf("accepts at most 1 argument, received %d", len(args))
			}
			return nil
		},
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

func (o *EnrollmentConfigOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)

	fs.StringVarP(&o.PrivateKey, "private-key", "p", o.PrivateKey, "Path to private key")
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

	params := &api.GetEnrollmentConfigParams{}
	if len(args) > 0 {
		params.Csr = &args[0]
	}
	response, err := c.GetEnrollmentConfigWithResponse(ctx, params)
	if err != nil {
		return err
	}

	err = validateHttpResponse(response.Body, response.StatusCode(), http.StatusOK)
	if err != nil {
		return fmt.Errorf("failed to get enrollment config: %w", err)
	}

	if len(o.PrivateKey) > 0 {
		privKey, err := os.ReadFile(o.PrivateKey)
		if err != nil {
			return err
		}

		encrypted, err := fccrypto.IsEncryptedPEMKey(privKey)
		if err != nil {
			return fmt.Errorf("invalid key specified: path: %s, error %w", o.PrivateKey, err)
		}
		if encrypted {
			pw, err := getPassword()
			if err != nil {
				return fmt.Errorf("getting password for encrypted key: %w", err)
			}
			privKey, err = fccrypto.DecryptKeyBytes(privKey, pw)
			if err != nil {
				return fmt.Errorf("unable to decrypt key: %w", err)
			}
		}
		response.JSON200.EnrollmentService.Authentication.ClientKeyData = base64.StdEncoding.EncodeToString(privKey)
	}

	marshalled, err := yaml.Marshal(response.JSON200)
	if err != nil {
		return fmt.Errorf("marshalling resource: %w", err)
	}
	fmt.Println(string(marshalled))
	return nil
}
