package cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"

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
		Use:   "enrollmentconfig NAME",
		Short: "Get enrollment config for devices",
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
	if len(o.PrivateKey) == 0 {
		return fmt.Errorf("must specify -p PRIVATE_KEY_FILE")
	}
	return nil
}

func (o *EnrollmentConfigOptions) Run(ctx context.Context, args []string) error {
	var pw []byte

	c, err := client.NewFromConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	privKey, err := os.ReadFile(o.PrivateKey)
	if err != nil {
		return err
	}

	response, err := c.EnrollmentConfigWithResponse(ctx, args[0])
	if err != nil {
		return err
	}

	err = validateHttpResponse(response.Body, response.StatusCode(), http.StatusOK)
	if err != nil {
		return fmt.Errorf("failed to get enrollment config: %w", err)
	}

	encrypted, err := fccrypto.IsEncryptedPEMKey(privKey)
	if err != nil {
		return fmt.Errorf("invalid key specified: path: %s, error %w", o.PrivateKey, err)
	}
	if encrypted {
		pw, err = getPassword()
		if err != nil {
			return fmt.Errorf("getting password for encrypted key: %w", err)
		}
		privKey, err = fccrypto.DecryptKeyBytes(privKey, pw)
		if err != nil {
			return fmt.Errorf("unable to decrypt key: %w", err)
		}
	}
	response.JSON200.EnrollmentService.Authentication.ClientKeyData = base64.StdEncoding.EncodeToString(privKey)

	marshalled, err := yaml.Marshal(response.JSON200)
	if err != nil {
		return fmt.Errorf("marshalling resource: %w", err)
	}
	fmt.Printf("%s\n", string(marshalled))
	return nil
}
