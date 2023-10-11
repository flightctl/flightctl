package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/pkg/client"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
)

var (
	resourceKinds = map[string]string{
		"device":            "",
		"enrollmentrequest": "",
		"fleet":             "",
	}
	resourceNameRegex = regexp.MustCompile(`^[a-zA-Z0-9\-]+$`)
	serverUrl         = "https://localhost:3333"
)

func main() {
	command := NewFlightctlCommand()
	if err := command.Execute(); err != nil {
		os.Exit(1)
	}
}

func parseAndValidateKindName(arg string) (string, string, error) {
	kind, name, _ := strings.Cut(arg, "/")
	kind = singular(kind)
	if _, ok := resourceKinds[kind]; !ok {
		return "", "", fmt.Errorf("invalid resource kind: %s", kind)
	}
	if len(name) > 0 && !resourceNameRegex.MatchString(name) {
		return "", "", fmt.Errorf("invalid resource name: %s", name)
	}
	return kind, name, nil
}

func singular(kind string) string {
	if strings.HasSuffix(kind, "s") {
		return kind[:len(kind)-1]
	}
	return kind
}

func NewFlightctlCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flightctl",
		Short: "flightctl controls the Flight Control device management service",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
			os.Exit(1)
		},
	}
	cmd.AddCommand(NewCmdGet())
	cmd.AddCommand(NewCmdApply())
	cmd.AddCommand(NewCmdDelete())
	return cmd
}

func NewCmdGet() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get",
		Short: "get resources",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, name, err := parseAndValidateKindName(args[0])
			if err != nil {
				return err
			}
			return RunGet(kind, name)
		},
	}
	return cmd
}

func NewCmdApply() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "apply resources",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, name, err := parseAndValidateKindName(args[0])
			if err != nil {
				return err
			}
			return RunApply(kind, name)
		},
	}
	return cmd
}

func NewCmdDelete() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "delete resources",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, name, err := parseAndValidateKindName(args[0])
			if err != nil {
				return err
			}
			return RunDelete(kind, name)
		},
	}
	return cmd
}

func getClient() (*client.ClientWithResponses, error) {
	certDir := config.CertificateDir()
	caCert, err := crypto.GetTLSCertificateConfig(filepath.Join(certDir, "csr-signer-ca.crt"), filepath.Join(certDir, "csr-signer-ca.key"))
	if err != nil {
		log.Fatalf("reading CA cert and key: %v", err)
	}
	clientCert, err := crypto.GetTLSCertificateConfig(filepath.Join(certDir, "client-bootstrap.crt"), filepath.Join(certDir, "client-bootstrap.key"))
	if err != nil {
		log.Fatalf("reading client cert and key: %v", err)
	}
	tlsConfig, err := crypto.TLSConfigForClient(caCert, clientCert)
	if err != nil {
		log.Fatalf("creating TLS config: %v", err)
	}
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}
	return client.NewClientWithResponses(serverUrl, client.WithHTTPClient(httpClient))
}

func RunGet(kind, name string) error {
	c, err := getClient()
	if err != nil {
		return fmt.Errorf("creating client: %v", err)
	}

	switch kind {
	case "device":
		if len(name) > 0 {
			response, err := c.ReadDeviceWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("reading device/%s: %v", name, err)
			}
			if response.HTTPResponse.StatusCode != http.StatusOK {
				return fmt.Errorf("reading device/%s: %s", name, response.HTTPResponse.Status)
			}

			marshalled, err := yaml.Marshal(response.JSON200)
			if err != nil {
				return fmt.Errorf("marshalling device: %v", err)
			}
			fmt.Printf("%s\n", string(marshalled))
		} else {
			params := &api.ListDevicesParams{}
			response, err := c.ListDevicesWithResponse(context.Background(), params)
			if err != nil {
				return fmt.Errorf("listing devices: %v", err)
			}
			if response.HTTPResponse.StatusCode != http.StatusOK {
				return fmt.Errorf("listing devices: %s", response.HTTPResponse.Status)
			}

			marshalled, err := yaml.Marshal(response.JSON200)
			if err != nil {
				return fmt.Errorf("marshalling device list: %v", err)
			}
			fmt.Printf("%s\n", string(marshalled))
		}
	case "enrollmentrequest":
		if len(name) > 0 {
			response, err := c.ReadEnrollmentRequestWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("reading enrollmentrequest/%s: %v", name, err)
			}
			if response.HTTPResponse.StatusCode != http.StatusOK {
				return fmt.Errorf("reading enrollmentrequest/%s: %s", name, response.HTTPResponse.Status)
			}

			marshalled, err := yaml.Marshal(response.JSON200)
			if err != nil {
				return fmt.Errorf("marshalling enrollmentrequest: %v", err)
			}
			fmt.Printf("%s\n", string(marshalled))
		} else {
			params := &api.ListEnrollmentRequestsParams{}
			response, err := c.ListEnrollmentRequestsWithResponse(context.Background(), params)
			if err != nil {
				return fmt.Errorf("listing enrollmentrequests: %v", err)
			}
			if response.HTTPResponse.StatusCode != http.StatusOK {
				return fmt.Errorf("listing enrollmentrequests: %s", response.HTTPResponse.Status)
			}

			marshalled, err := yaml.Marshal(response.JSON200)
			if err != nil {
				return fmt.Errorf("marshalling enrollmentrequest list: %v", err)
			}
			fmt.Printf("%s\n", string(marshalled))
		}
	case "fleet":
		if len(name) > 0 {
			response, err := c.ReadFleetWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("reading fleet/%s: %v", name, err)
			}
			if response.HTTPResponse.StatusCode != http.StatusOK {
				return fmt.Errorf("reading fleet/%s: %s", name, response.HTTPResponse.Status)
			}

			marshalled, err := yaml.Marshal(response.JSON200)
			if err != nil {
				return fmt.Errorf("marshalling fleet: %v", err)
			}
			fmt.Printf("%s\n", string(marshalled))
		} else {
			params := &api.ListFleetsParams{}
			response, err := c.ListFleetsWithResponse(context.Background(), params)
			if err != nil {
				return fmt.Errorf("listing fleets: %v", err)
			}
			if response.HTTPResponse.StatusCode != http.StatusOK {
				return fmt.Errorf("listing fleets: %s", response.HTTPResponse.Status)
			}

			marshalled, err := yaml.Marshal(response.JSON200)
			if err != nil {
				return fmt.Errorf("marshalling fleet list: %v", err)
			}
			fmt.Printf("%s\n", string(marshalled))
		}
	default:
		return fmt.Errorf("unsupported resource kind: %s", kind)
	}

	return nil
}

func RunApply(kind, name string) error {
	return nil
}

func RunDelete(kind, name string) error {
	c, err := getClient()
	if err != nil {
		return fmt.Errorf("creating client: %v", err)
	}

	switch kind {
	case "device":
		if len(name) > 0 {
			response, err := c.DeleteDeviceWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("deleting device/%s: %v", name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteDevicesWithResponse(context.Background())
			if err != nil {
				return fmt.Errorf("deleting devices: %v", err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	case "enrollmentrequest":
		if len(name) > 0 {
			response, err := c.DeleteEnrollmentRequestWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("deleting enrollmentrequest/%s: %v", name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteEnrollmentRequestsWithResponse(context.Background())
			if err != nil {
				return fmt.Errorf("deleting enrollmentrequests: %v", err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	case "fleet":
		if len(name) > 0 {
			response, err := c.DeleteFleetWithResponse(context.Background(), name)
			if err != nil {
				return fmt.Errorf("deleting fleet/%s: %v", name, err)
			}
			fmt.Printf("%s\n", response.Status())
		} else {
			response, err := c.DeleteFleetsWithResponse(context.Background())
			if err != nil {
				return fmt.Errorf("deleting fleets: %v", err)
			}
			fmt.Printf("%s\n", response.Status())
		}
	default:
		return fmt.Errorf("unsupported resource kind: %s", kind)
	}

	return nil
}
