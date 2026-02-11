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

	"github.com/flightctl/flightctl/api/versioning"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	apiclientv1alpha1 "github.com/flightctl/flightctl/internal/api/client/v1alpha1"
	imagebuilderclient "github.com/flightctl/flightctl/internal/api/imagebuilder/client"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

const (
	apiVersionPrefix = "flightctl.io/"
)

var (
	fileExtensions  = []string{".json", ".yaml", ".yml"}
	inputExtensions = append(fileExtensions, "stdin")

	// alphaResources defines resource kinds that require v1alpha1 apiVersion.
	alphaResources = map[ResourceKind]struct{}{
		CatalogKind:     {},
		CatalogItemKind: {},
	}
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
			ctx, cancel := o.WithTimeout(cmd.Context())
			defer cancel()
			return o.Run(ctx, args)
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
	c, err := o.BuildClient()
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}
	c.Start(ctx)
	defer c.Stop()

	// Build imagebuilder client (may be nil if not configured)
	var ibClient *client.ImageBuilderClient
	ibClient, _ = o.BuildImageBuilderClient() // Ignore error; we'll check per-resource
	if ibClient != nil {
		ibClient.Start(ctx)
		defer ibClient.Stop()
	}

	errs := make([]error, 0)
	for _, filename := range o.Filenames {
		switch {
		case filename == "-":
			errs = append(errs, applyFromReader(ctx, c, ibClient, "<stdin>", os.Stdin, o.DryRun)...)
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
					errs = append(errs, applyFromReader(ctx, c, ibClient, path, r, o.DryRun)...)
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

// applyResult holds the result of applying a single resource
type applyResult struct {
	httpResponse *http.Response
	message      string
	err          error
}

// validateResourceAPIVersion validates that the apiVersion in the resource
// matches the expected version for the resource kind.
func validateResourceAPIVersion(resource genericResource, kind ResourceKind) error {
	apiVersion, _ := resource["apiVersion"].(string)

	// Check if this is an alpha resource
	if _, isAlpha := alphaResources[kind]; isAlpha {
		expectedVersion := apiVersionPrefix + versioning.V1Alpha1
		if apiVersion != expectedVersion {
			return fmt.Errorf("%s requires apiVersion %q, got %q", kind, expectedVersion, apiVersion)
		}
	}

	return nil
}

func applyFromReader(ctx context.Context, c *client.Client, ibClient *client.ImageBuilderClient, filename string, r io.Reader, dryRun bool) []error {
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
		if applyErr := applySingleResource(ctx, c, ibClient, filename, resource, dryRun); applyErr != nil {
			errs = append(errs, applyErr...)
		}
	}
	return errs
}

func applySingleResource(ctx context.Context, c *client.Client, ibClient *client.ImageBuilderClient, filename string, resource genericResource, dryRun bool) []error {
	kindLike, ok := resource["kind"].(string)
	if !ok {
		return []error{fmt.Errorf("%s: skipping resource of unspecified kind: %v", filename, resource)}
	}
	metadata, ok := resource["metadata"].(map[string]interface{})
	if !ok {
		return []error{fmt.Errorf("%s: skipping resource of unspecified metadata: %v", filename, resource)}
	}
	resourceName, ok := metadata["name"].(string)
	if !ok {
		return []error{fmt.Errorf("%s: skipping resource of unspecified resource name: %v", filename, resource)}
	}
	if resourceName == "" {
		return []error{fmt.Errorf("%s: metadata.name must not be empty", filename)}
	}

	kind, _ := ResourceKindFromString(kindLike)

	// Validate apiVersion matches expected version for the resource kind
	if err := validateResourceAPIVersion(resource, kind); err != nil {
		return []error{fmt.Errorf("%s: %w", filename, err)}
	}

	if dryRun {
		fmt.Printf("%s: applying %s/%s (dry run only)\n", filename, strings.ToLower(kindLike), resourceName)
		return nil
	}

	fmt.Printf("%s: applying %s/%s: ", filename, strings.ToLower(kindLike), resourceName)
	buf, err := json.Marshal(resource)
	if err != nil {
		return []error{fmt.Errorf("%s: skipping resource of kind %q: %w", filename, kindLike, err)}
	}
	result := applyResourceByKind(ctx, c, ibClient, kind, resourceName, buf)

	var errs []error
	if result.err != nil {
		errs = append(errs, result.err)
	}

	if result.httpResponse != nil {
		fmt.Printf("%s\n", result.httpResponse.Status)
		if result.httpResponse.StatusCode != http.StatusOK && result.httpResponse.StatusCode != http.StatusCreated {
			errs = append(errs, fmt.Errorf("%s: failed to apply %s/%s: %s", strings.ToLower(kindLike), filename, resourceName, result.httpResponse.Status))
			fmt.Printf("%s\n", result.message)
		}
	}

	return errs
}

func applyResourceByKind(ctx context.Context, c *client.Client, ibClient *client.ImageBuilderClient, kind ResourceKind, resourceName string, buf []byte) applyResult {
	switch kind {
	case DeviceKind:
		response, err := c.ReplaceDeviceWithBodyWithResponse(ctx, resourceName, "application/json", bytes.NewReader(buf))
		return extractApplyResult(response, err)
	case EnrollmentRequestKind:
		response, err := c.ReplaceEnrollmentRequestWithBodyWithResponse(ctx, resourceName, "application/json", bytes.NewReader(buf))
		return extractApplyResult(response, err)
	case FleetKind:
		response, err := c.ReplaceFleetWithBodyWithResponse(ctx, resourceName, "application/json", bytes.NewReader(buf))
		return extractApplyResult(response, err)
	case RepositoryKind:
		response, err := c.ReplaceRepositoryWithBodyWithResponse(ctx, resourceName, "application/json", bytes.NewReader(buf))
		return extractApplyResult(response, err)
	case ResourceSyncKind:
		response, err := c.ReplaceResourceSyncWithBodyWithResponse(ctx, resourceName, "application/json", bytes.NewReader(buf))
		return extractApplyResult(response, err)
	case CertificateSigningRequestKind:
		response, err := c.ReplaceCertificateSigningRequestWithBodyWithResponse(ctx, resourceName, "application/json", bytes.NewReader(buf))
		return extractApplyResult(response, err)
	case AuthProviderKind:
		response, err := c.ReplaceAuthProviderWithBodyWithResponse(ctx, resourceName, "application/json", bytes.NewReader(buf))
		return extractApplyResult(response, err)
	case ImageBuildKind:
		if ibClient == nil {
			return applyResult{err: fmt.Errorf("imagebuilder service is not configured. Please configure 'imageBuilderService.server' in your client config")}
		}
		response, err := ibClient.CreateImageBuildWithBodyWithResponse(ctx, "application/json", bytes.NewReader(buf))
		return extractApplyResult(response, err)
	case ImageExportKind:
		if ibClient == nil {
			return applyResult{err: fmt.Errorf("imagebuilder service is not configured. Please configure 'imageBuilderService.server' in your client config")}
		}
		response, err := ibClient.CreateImageExportWithBodyWithResponse(ctx, "application/json", bytes.NewReader(buf))
		return extractApplyResult(response, err)
	case CatalogKind:
		response, err := c.V1Alpha1().ReplaceCatalogWithBodyWithResponse(ctx, resourceName, "application/json", bytes.NewReader(buf))
		return extractApplyResult(response, err)
	case CatalogItemKind:
		catalogName, err := extractCatalogFromMetadata(buf)
		if err != nil {
			return applyResult{err: err}
		}
		response, err := c.V1Alpha1().ReplaceCatalogItemWithBodyWithResponse(ctx, catalogName, resourceName, "application/json", bytes.NewReader(buf))
		return extractApplyResult(response, err)
	default:
		return applyResult{err: fmt.Errorf("skipping resource of unknown kind %q", kind)}
	}
}

// extractApplyResult extracts HTTP response and body from various response types
func extractApplyResult(response interface{}, err error) applyResult {
	if err != nil {
		return applyResult{err: err}
	}
	if response == nil {
		return applyResult{}
	}

	// Use type switch to extract fields from different response types
	switch r := response.(type) {
	case *apiclient.ReplaceDeviceResponse:
		return applyResult{httpResponse: r.HTTPResponse, message: string(r.Body)}
	case *apiclient.ReplaceEnrollmentRequestResponse:
		return applyResult{httpResponse: r.HTTPResponse, message: string(r.Body)}
	case *apiclient.ReplaceFleetResponse:
		return applyResult{httpResponse: r.HTTPResponse, message: string(r.Body)}
	case *apiclient.ReplaceRepositoryResponse:
		return applyResult{httpResponse: r.HTTPResponse, message: string(r.Body)}
	case *apiclient.ReplaceResourceSyncResponse:
		return applyResult{httpResponse: r.HTTPResponse, message: string(r.Body)}
	case *apiclient.ReplaceCertificateSigningRequestResponse:
		return applyResult{httpResponse: r.HTTPResponse, message: string(r.Body)}
	case *apiclient.ReplaceAuthProviderResponse:
		return applyResult{httpResponse: r.HTTPResponse, message: string(r.Body)}
	case *apiclientv1alpha1.ReplaceCatalogResponse:
		return applyResult{httpResponse: r.HTTPResponse, message: string(r.Body)}
	case *apiclientv1alpha1.ReplaceCatalogItemResponse:
		return applyResult{httpResponse: r.HTTPResponse, message: string(r.Body)}
	case *imagebuilderclient.CreateImageBuildResponse:
		return applyResult{httpResponse: r.HTTPResponse, message: string(r.Body)}
	case *imagebuilderclient.CreateImageExportResponse:
		return applyResult{httpResponse: r.HTTPResponse, message: string(r.Body)}
	default:
		return applyResult{}
	}
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

func extractCatalogFromMetadata(buf []byte) (string, error) {
	var resource struct {
		Metadata struct {
			Catalog string `json:"catalog"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(buf, &resource); err != nil {
		return "", fmt.Errorf("failed to parse resource: %w", err)
	}
	if resource.Metadata.Catalog == "" {
		return "", fmt.Errorf("catalogitem requires metadata.catalog to specify the parent catalog")
	}
	return resource.Metadata.Catalog, nil
}
