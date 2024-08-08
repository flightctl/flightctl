package cli

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
	"strings"

	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

var (
	fileExtensions  = []string{".json", ".yaml", ".yml"}
	inputExtensions = append(fileExtensions, "stdin")
)

type ApplyOptions struct {
	GlobalOptions

	Filenames []string
	DryRun    bool
	Recursive bool
}

func DefaultApplyOptions() *ApplyOptions {
	return &ApplyOptions{
		GlobalOptions: DefaultGlobalOptions(),
		Filenames:     []string{},
		DryRun:        false,
		Recursive:     false,
	}
}

func NewCmdApply() *cobra.Command {
	o := DefaultApplyOptions()
	cmd := &cobra.Command{
		Use:   "apply -f FILENAME",
		Short: "Apply a configuration to a resource by file name or stdin.",
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

func (o *ApplyOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)

	fs.StringSliceVarP(&o.Filenames, "filename", "f", o.Filenames, "The files or directory that contain the resources to apply.")
	annotations := make([]string, 0, len(fileExtensions))
	for _, ext := range fileExtensions {
		annotations = append(annotations, strings.TrimLeft(ext, "."))
	}
	err := fs.SetAnnotation("filename", cobra.BashCompFilenameExt, annotations)
	if err != nil {
		log.Fatalf("setting filename flag annotation: %v", err)
	}
	fs.BoolVarP(&o.DryRun, "dry-run", "", o.DryRun, "Only print the object that would be sent, without sending it.")
	fs.BoolVarP(&o.Recursive, "recursive", "R", o.Recursive, "Process the directory used in -f, --filename recursively.")
}

func (o *ApplyOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}

	return nil
}

func (o *ApplyOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}

	if len(o.Filenames) == 0 {
		return fmt.Errorf("must specify -f FILENAME")
	}
	if len(args) > 0 {
		return fmt.Errorf("unexpected arguments: %v (did you forget to quote wildcards?)", args)
	}
	return nil
}

func (o *ApplyOptions) Run(ctx context.Context, args []string) error {
	c, err := client.NewFromConfigFile(o.ConfigFilePath)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	errs := make([]error, 0)
	for _, filename := range o.Filenames {
		switch {
		case filename == "-":
			errs = append(errs, applyFromReader(ctx, c, "<stdin>", os.Stdin, o.DryRun)...)
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
					errs = append(errs, fmt.Errorf("the path %q cannot be accessed: %w", filename, err))
					continue
				}
				err = filepath.Walk(filename, func(path string, fi os.FileInfo, err error) error {
					if err != nil {
						return err
					}

					if fi.IsDir() {
						if path != filename && !o.Recursive {
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
					errs = append(errs, applyFromReader(ctx, c, path, r, o.DryRun)...)
					return nil
				})
				if err != nil {
					errs = append(errs, fmt.Errorf("error walking %q: %w", filename, err))
				}
			}
		}
	}
	return errors.Join(errs...)
}

type genericResource map[string]interface{}

func applyFromReader(ctx context.Context, client *apiclient.ClientWithResponses, filename string, r io.Reader, dryRun bool) []error {
	decoder := yamlutil.NewYAMLOrJSONDecoder(r, 100)
	resources := []genericResource{}

	var err error
	for {
		var resource genericResource
		err = decoder.Decode(&resource)
		if err != nil {
			break
		}
		resources = append(resources, resource)
	}
	if !errors.Is(err, io.EOF) {
		return []error{err}
	}

	errs := make([]error, 0)
	for _, resource := range resources {
		kind, ok := resource["kind"].(string)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: skipping resource of unspecified kind: %v", filename, resource))
			continue
		}
		metadata, ok := resource["metadata"].(map[string]interface{})
		if !ok {
			errs = append(errs, fmt.Errorf("%s: skipping resource of unspecified metadata: %v", filename, resource))
			continue
		}
		resourceName, ok := metadata["name"].(string)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: skipping resource of unspecified resource name: %v", filename, resource))
			continue
		}

		if dryRun {
			fmt.Printf("%s: applying %s/%s (dry run only)\n", strings.ToLower(kind), filename, resourceName)
			continue
		}
		buf, err := json.Marshal(resource)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: skipping resource of kind %q: %w", filename, kind, err))
		}

		var httpResponse *http.Response

		switch strings.ToLower(kind) {
		case DeviceKind:
			var response *apiclient.ReplaceDeviceResponse
			response, err = client.ReplaceDeviceWithBodyWithResponse(ctx, resourceName, "application/json", bytes.NewReader(buf))
			if response != nil {
				httpResponse = response.HTTPResponse
			}
		case EnrollmentRequestKind:
			var response *apiclient.ReplaceEnrollmentRequestResponse
			response, err = client.ReplaceEnrollmentRequestWithBodyWithResponse(ctx, resourceName, "application/json", bytes.NewReader(buf))
			if response != nil {
				httpResponse = response.HTTPResponse
			}
		case FleetKind:
			var response *apiclient.ReplaceFleetResponse
			response, err = client.ReplaceFleetWithBodyWithResponse(ctx, resourceName, "application/json", bytes.NewReader(buf))
			if response != nil {
				httpResponse = response.HTTPResponse
			}
		case RepositoryKind:
			var response *apiclient.ReplaceRepositoryResponse
			response, err = client.ReplaceRepositoryWithBodyWithResponse(ctx, resourceName, "application/json", bytes.NewReader(buf))
			if response != nil {
				httpResponse = response.HTTPResponse
			}
		case ResourceSyncKind:
			var response *apiclient.ReplaceResourceSyncResponse
			response, err = client.ReplaceResourceSyncWithBodyWithResponse(ctx, resourceName, "application/json", bytes.NewReader(buf))
			if response != nil {
				httpResponse = response.HTTPResponse
			}
		default:
			err = fmt.Errorf("%s: skipping resource of unknown kind %q: %v", filename, kind, resource)
		}

		if err != nil {
			errs = append(errs, err)
		}

		if httpResponse != nil {
			// bad HTTP Responses don't generate an error on the OpenAPI client, we need to check the status code manually
			if httpResponse.StatusCode != http.StatusOK && httpResponse.StatusCode != http.StatusCreated {
				errs = append(errs, fmt.Errorf("%s: failed to apply %s/%s: %s", strings.ToLower(kind), filename, resourceName, httpResponse.Status))
			} else {
				fmt.Printf("%s: applied %s/%s: %s", strings.ToLower(kind), filename, resourceName, httpResponse.Status)
			}
		}
	}
	return errs
}

func expandIfFilePattern(pattern string) ([]string, error) {
	if _, err := os.Stat(pattern); os.IsNotExist(err) {
		matches, err := filepath.Glob(pattern)
		if err == nil && len(matches) == 0 {
			return nil, fmt.Errorf("the path %q does not exist", pattern)
		}
		if err == filepath.ErrBadPattern {
			return nil, fmt.Errorf("pattern %q is not valid: %w", pattern, err)
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
