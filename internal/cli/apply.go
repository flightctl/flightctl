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
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

var (
	fileExtensions  = []string{".json", ".yaml", ".yml"}
	inputExtensions = append(fileExtensions, "stdin")
)

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
	err := flags.SetAnnotation("filename", cobra.BashCompFilenameExt, annotations)
	if err != nil {
		log.Fatalf("setting filename flag annotation: %v", err)
	}
	flags.BoolVarP(&o.DryRun, "dry-run", "", o.DryRun, "only print the object that would be sent, without sending it")
	flags.BoolVarP(&o.Recursive, "recursive", "R", o.Recursive, "process the directory used in -f, --filename recursively")

	return cmd
}

type genericResource map[string]interface{}

func applyFromReader(client *apiclient.ClientWithResponses, filename string, r io.Reader, dryRun bool) []error {
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
		fmt.Printf("%s: applying %s/%s: ", strings.ToLower(kind), filename, resourceName)
		buf, err := json.Marshal(resource)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: skipping resource of kind %q: %v", filename, kind, err))
		}

		var httpResponse *http.Response

		switch kind {
		case "Device":
			var response *apiclient.ReplaceDeviceResponse
			response, err = client.ReplaceDeviceWithBodyWithResponse(context.Background(), resourceName, "application/json", bytes.NewReader(buf))
			if response != nil {
				httpResponse = response.HTTPResponse
			}

		case "EnrollmentRequest":
			var response *apiclient.ReplaceEnrollmentRequestResponse
			response, err = client.ReplaceEnrollmentRequestWithBodyWithResponse(context.Background(), resourceName, "application/json", bytes.NewReader(buf))
			if response != nil {
				httpResponse = response.HTTPResponse
			}

		case "Fleet":
			var response *apiclient.ReplaceFleetResponse
			response, err = client.ReplaceFleetWithBodyWithResponse(context.Background(), resourceName, "application/json", bytes.NewReader(buf))
			if response != nil {
				httpResponse = response.HTTPResponse
			}
		case "Repository":
			var response *apiclient.ReplaceRepositoryResponse
			response, err = client.ReplaceRepositoryWithBodyWithResponse(context.Background(), resourceName, "application/json", bytes.NewReader(buf))
			if response != nil {
				httpResponse = response.HTTPResponse
			}
		case "ResourceSync":
			var response *apiclient.ReplaceResourceSyncResponse
			response, err = client.ReplaceResourceSyncWithBodyWithResponse(context.Background(), resourceName, "application/json", bytes.NewReader(buf))
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
			fmt.Printf("%s\n", httpResponse.Status)
			// bad HTTP Responses don't generate an error on the OpenAPI client, we need to check the status code manually
			if httpResponse.StatusCode != http.StatusOK && httpResponse.StatusCode != http.StatusCreated {
				errs = append(errs, fmt.Errorf("%s: failed to apply %s/%s: %v", strings.ToLower(kind), filename, resourceName, httpResponse.Status))
			}
		}

	}
	return errs
}

func RunApply(filenames []string, recursive bool, dryRun bool) error {
	client, err := client.NewFromConfigFile(defaultClientConfigFile)
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
