package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	apiClient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type LoginOptions struct {
	GlobalOptions
	Token string
}

func DefaultLoginOptions() *LoginOptions {
	return &LoginOptions{
		GlobalOptions: DefaultGlobalOptions(),
		Token:         "",
	}
}

func NewCmdLogin() *cobra.Command {
	o := DefaultLoginOptions()
	cmd := &cobra.Command{
		Use:   "login [URL] --token TOKEN",
		Short: "Login to flight control",
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

func (o *LoginOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)

	fs.StringVarP(&o.Token, "token", "t", o.Token, "Bearer token for authentication to the API server")
}

func (o *LoginOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}
	return nil
}

func (o *LoginOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}
	return nil
}

type OauthServerResponse struct {
	TokenEndpoint string `json:"token_endpoint"`
}

func (o *LoginOptions) Run(ctx context.Context, args []string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	configFilePath := filepath.Join(homeDir, ".flightctl", "client.yaml")
	config, err := client.ParseConfigFile(configFilePath)
	if err != nil {
		return err
	}

	config.Service.Server = args[0]

	httpClient, err := client.NewHTTPClientFromConfig(config)
	if err != nil {
		return err
	}
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	c, err := apiClient.NewClientWithResponses(config.Service.Server, apiClient.WithHTTPClient(httpClient))
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}
	resp, err := c.TokenRequestWithResponse(ctx)
	if err != nil {
		return err
	}

	if resp.StatusCode() == http.StatusTeapot {
		fmt.Println("Auth is disabled")
		err = config.Persist(o.ConfigFilePath)
		if err != nil {
			return fmt.Errorf("persisting client config: %w", err)
		}
		return nil
	}

	if o.Token == "" {
		fmt.Printf("You must obtain an API token by visiting %s/api/v1/token/request\n", config.Service.Server)
		fmt.Printf("Then login via flightctl login %s --token=<token>\n", config.Service.Server)
		return nil
	}

	config.AuthInfo.Token = o.Token
	c, err = client.NewFromConfig(config)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	res, err := c.TokenValidateWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("validating token: %w", err)
	}

	if res.StatusCode() != http.StatusOK {
		return errors.New("the token provided is invalid or expired")
	}

	err = config.Persist(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("persisting client config: %w", err)
	}
	fmt.Println("Login successful.")
	return nil
}
