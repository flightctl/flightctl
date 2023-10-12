package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"gopkg.in/yaml.v3"
)

var (
	resourceKinds = map[string]string{
		"device":            "",
		"enrollmentrequest": "",
		"fleet":             "",
	}
	resourceNameRegex = regexp.MustCompile(`^[a-zA-Z0-9\-]+$`)
	serverUrl         = "https://localhost:3333"
	fileExtensions    = []string{".json", ".yaml", ".yml"}
	inputExtensions   = append(fileExtensions, "stdin")
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
		SilenceUsage: true,
	}
	return cmd
}

type ApplyOptions struct {
	Filenames []string
	DryRun    bool
	Recursive bool
}

func NewCmdApply() *cobra.Command {
	o := &ApplyOptions{Filenames: []string{}, DryRun: false, Recursive: false}

	cmd := &cobra.Command{
		Use:                   "apply -f FILENAME",
		DisableFlagsInUseLine: true,
		Short:                 "apply a configuration to a resource by file name or stdin",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Lookup("filename").Changed {
				return fmt.Errorf("must specify -f FILENAME")
			}
			if len(args) > 0 {
				return fmt.Errorf("unexpected arguments: %v (did you forget to quote wildcards?)", args)
			}
			return RunApply(o.Filenames, o.Recursive, o.DryRun)
		},
		SilenceUsage: true,
	}

	flags := cmd.Flags()
	flags.StringSliceVarP(&o.Filenames, "filename", "f", o.Filenames, "read resources from file or directory")
	annotations := make([]string, 0, len(fileExtensions))
	for _, ext := range fileExtensions {
		annotations = append(annotations, strings.TrimLeft(ext, "."))
	}
	flags.SetAnnotation("filename", cobra.BashCompFilenameExt, annotations)
	flags.BoolVarP(&o.DryRun, "dry-run", "", o.DryRun, "only print the object that would be sent, without sending it")
	flags.BoolVarP(&o.Recursive, "recursive", "R", o.Recursive, "process the directory used in -f, --filename recursively")

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
		SilenceUsage: true,
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

func expandIfFilePattern(pattern string) ([]string, error) {
	if _, err := os.Stat(pattern); os.IsNotExist(err) {
		matches, err := filepath.Glob(pattern)
		if err == nil && len(matches) == 0 {
			return nil, fmt.Errorf("the path %q does not exist", pattern)
		}
		if err == filepath.ErrBadPattern {
			return nil, fmt.Errorf("pattern %q is not valid: %v", pattern, err)
		}
		return matches, err
	}
	return []string{pattern}, nil
}

func ignoreFile(path string, extensions []string) bool {
	if len(extensions) == 0 {
		return false
	}
	ext := filepath.Ext(path)
	for _, s := range extensions {
		if s == ext {
			return false
		}
	}
	return true
}

type gemericResource struct {
	ApiVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Metadata   api.ObjectMeta `yaml:"metadata"`
	Spec       interface{}    `yaml:"spec,omitempty"`
	Status     interface{}    `yaml:"status,omitempty"`
}

func applyFromReader(client *client.ClientWithResponses, filename string, r io.Reader, dryRun bool) []error {
	var buf bytes.Buffer
	r = io.TeeReader(r, &buf)

	d := yaml.NewDecoder(r)
	d.KnownFields(true)

	errs := make([]error, 0)
	for {
		resource := &gemericResource{}
		err := d.Decode(&resource)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("error decoding resource from %q: %v", filename, err))
			continue
		}
		if r == nil {
			errs = append(errs, fmt.Errorf("error decoding resource from %q, skipping", filename))
			continue
		}

		switch resource.Kind {
		case "Device":
			device := &api.Device{}
			err := yaml.Unmarshal(buf.Bytes(), device)
			if err != nil {
				errs = append(errs, fmt.Errorf("decoding Device resource from %s: %v", filename, err))
				continue
			}
			if dryRun {
				fmt.Printf("%s: applying device/%s (dry run only)\n", filename, device.Metadata.Name)
			} else {
				var body bytes.Buffer
				if err := json.NewEncoder(&body).Encode(device); err != nil {
					errs = append(errs, err)
					continue
				}
				fmt.Printf("%s: applying device/%s: ", filename, device.Metadata.Name)
				response, err := client.ReplaceDeviceWithBodyWithResponse(context.Background(), device.Metadata.Name, "application/json", bytes.NewReader(body.Bytes()))
				if err != nil {
					errs = append(errs, err)
					continue
				}
				fmt.Printf("%s\n", response.HTTPResponse.Status)
			}
		case "EnrollmentRequest":
			enrollmentRequest := &api.EnrollmentRequest{}
			err := yaml.Unmarshal(buf.Bytes(), enrollmentRequest)
			if err != nil {
				errs = append(errs, fmt.Errorf("decoding EnrollmentRequest resource from %s: %v", filename, err))
				continue
			}
			if dryRun {
				fmt.Printf("%s: applying enrollmentrequest/%s (dry run only)\n", filename, enrollmentRequest.Metadata.Name)
			} else {
				var body bytes.Buffer
				if err := json.NewEncoder(&body).Encode(enrollmentRequest); err != nil {
					errs = append(errs, err)
					continue
				}
				fmt.Printf("%s: applying enrollmentrequest/%s: ", filename, enrollmentRequest.Metadata.Name)
				response, err := client.ReplaceEnrollmentRequestWithBodyWithResponse(context.Background(), enrollmentRequest.Metadata.Name, "application/json", bytes.NewReader(body.Bytes()))
				if err != nil {
					errs = append(errs, err)
					continue
				}
				fmt.Printf("%s\n", response.HTTPResponse.Status)
			}
		case "Fleet":
			fleet := &api.Fleet{}
			err := yaml.Unmarshal(buf.Bytes(), fleet)
			if err != nil {
				errs = append(errs, fmt.Errorf("decoding Fleet resource from %s: %v", filename, err))
				continue
			}
			if dryRun {
				fmt.Printf("%s: applying fleet/%s (dry run only)\n", filename, fleet.Metadata.Name)
			} else {
				var body bytes.Buffer
				if err := json.NewEncoder(&body).Encode(fleet); err != nil {
					errs = append(errs, err)
					continue
				}
				fmt.Printf("%s: applying fleet/%s: ", filename, fleet.Metadata.Name)
				response, err := client.ReplaceFleetWithBodyWithResponse(context.Background(), fleet.Metadata.Name, "application/json", bytes.NewReader(body.Bytes()))
				if err != nil {
					errs = append(errs, err)
					continue
				}
				fmt.Printf("%s\n", response.HTTPResponse.Status)
			}
		default:
			fmt.Printf("unsupported resource kind %q\n", resource.Kind)
		}
	}
	return errs
}

func RunApply(filenames []string, recursive bool, dryRun bool) error {
	client, err := getClient()
	if err != nil {
		return fmt.Errorf("creating client: %v", err)
	}

	errs := make([]error, 0)
	for _, filename := range filenames {
		switch {
		case filename == "-":
			errs = append(errs, applyFromReader(client, "<stdin>", os.Stdin, dryRun)...)
		default:
			expandedFilenames, err := expandIfFilePattern(filename)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			for _, filename := range expandedFilenames {
				_, err := os.Stat(filename)
				if os.IsNotExist(err) {
					errs = append(errs, fmt.Errorf("the path %q does not exist", filename))
					continue
				}
				if err != nil {
					errs = append(errs, fmt.Errorf("the path %q cannot be accessed: %v", filename, err))
					continue
				}
				err = filepath.Walk(filename, func(path string, fi os.FileInfo, err error) error {
					if err != nil {
						return err
					}

					if fi.IsDir() {
						if path != filename && !recursive {
							return filepath.SkipDir
						}
						return nil
					}
					// Don't check extension if the filepath was passed explicitly
					if path != filename && ignoreFile(path, inputExtensions) {
						return nil
					}

					r, err := os.Open(path)
					if err != nil {
						return nil
					}
					defer r.Close()
					errs = append(errs, applyFromReader(client, path, r, dryRun)...)
					return nil
				})
				if err != nil {
					errs = append(errs, fmt.Errorf("error walking %q: %v", filename, err))
				}
			}
		}
	}
	return errors.Join(errs...)
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
