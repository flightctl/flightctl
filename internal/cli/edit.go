package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"sigs.k8s.io/yaml"
)

type EditOptions struct {
	GlobalOptions

	FleetName string
	Editor    string
	Output    string
}

func DefaultEditOptions() *EditOptions {
	return &EditOptions{
		GlobalOptions: DefaultGlobalOptions(),
		FleetName:     "",
		Editor:        "",
		Output:        "yaml",
	}
}

// getValidEditResourceKinds returns the resource kinds that support editing
func getValidEditResourceKinds() []string {
	return []string{
		DeviceKind,
		FleetKind,
		RepositoryKind,
	}
}

func NewCmdEdit() *cobra.Command {
	o := DefaultEditOptions()
	cmd := &cobra.Command{
		Use:   "edit (TYPE NAME | TYPE/NAME)",
		Short: "Edit a resource in your default editor.",
		Long: `Edit a resource from your default command-line editor.

The edit command allows you to directly edit an API resource. It opens the editor defined by --editor,
then KUBE_EDITOR, then VISUAL, then EDITOR environment variables, or falls back to 'vi'.

Edits are applied using JSON patch operations for efficiency.

Editing uses the API version used to fetch the resource.

The default format is YAML. To edit in JSON, specify "-o json".

In the event an error occurs while updating, a temporary file will be created on disk
that contains your unapplied changes. The most common error when updating a resource
is another editor changing the resource on the server. When this occurs, you will have
to apply your changes to the newer version of the resource, or update your temporary
saved copy to include the latest resource version.`,
		Args:      cobra.RangeArgs(1, 2),
		ValidArgs: getValidEditResourceKinds(),
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

func (o *EditOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)

	fs.StringVar(&o.FleetName, "fleetname", o.FleetName, "Fleet name for accessing templateversions (use only when editing templateversions).")
	fs.StringVar(&o.Editor, "editor", o.Editor, "Editor to use for editing the resource. If not specified, uses KUBE_EDITOR, then VISUAL, then EDITOR environment variables, or falls back to 'vi'.")
	fs.StringVarP(&o.Output, "output", "o", o.Output, "Output format. One of: (yaml, json). Default: yaml")
}

func (o *EditOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}

	// Determine the editor to use
	if o.Editor == "" {
		if editor := os.Getenv("KUBE_EDITOR"); editor != "" {
			o.Editor = editor
		} else if editor := os.Getenv("VISUAL"); editor != "" {
			o.Editor = editor
		} else if editor := os.Getenv("EDITOR"); editor != "" {
			o.Editor = editor
		} else {
			// Default editor
			o.Editor = "vi"
		}
	}

	return nil
}

func (o *EditOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}

	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}

	// Handle both TYPE/NAME and TYPE NAME formats
	if len(name) == 0 && len(args) < 2 {
		return fmt.Errorf("you must specify a resource name to edit")
	}
	if len(name) > 0 && len(args) > 1 {
		return fmt.Errorf("invalid format: cannot mix TYPE/NAME syntax with additional resource names. Use either 'edit TYPE/NAME' or 'edit TYPE NAME'")
	}

	// Check if resource type supports editing (only Device, Fleet, and Repository are supported)
	switch kind {
	case DeviceKind, FleetKind, RepositoryKind:
		// These are supported for editing
	case EventKind:
		return fmt.Errorf("you cannot edit events")
	case OrganizationKind:
		return fmt.Errorf("you cannot edit organizations")
	case TemplateVersionKind:
		return fmt.Errorf("you cannot edit templateversions")
	case CertificateSigningRequestKind:
		return fmt.Errorf("you cannot edit certificatesigningrequests")
	case EnrollmentRequestKind:
		return fmt.Errorf("you cannot edit enrollmentrequests")
	case ResourceSyncKind:
		return fmt.Errorf("you cannot edit resourcesyncs")
	default:
		return fmt.Errorf("unsupported resource kind for editing: %s (PATCH not supported)", kind)
	}

	// Validate output format
	if o.Output != "yaml" && o.Output != "json" {
		return fmt.Errorf("output format must be one of (yaml, json), got: %s", o.Output)
	}

	return nil
}

func (o *EditOptions) Run(ctx context.Context, args []string) error {
	clientWithResponses, err := o.BuildClient()
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}

	kind, name, err := parseAndValidateKindName(args[0])
	if err != nil {
		return err
	}

	// Handle TYPE NAME format
	if len(name) == 0 && len(args) >= 2 {
		name = args[1]
	}

	// Fetch the current resource
	fetchCtx, fetchCancel := o.WithTimeout(ctx)
	originalResource, err := o.fetchResource(fetchCtx, clientWithResponses, kind, name)
	fetchCancel()
	if err != nil {
		return fmt.Errorf("fetching %s/%s: %w", kind, name, err)
	}

	// Convert to the specified format for editing
	originalContent, err := o.resourceToFormat(originalResource)
	if err != nil {
		return fmt.Errorf("converting resource to %s: %w", o.Output, err)
	}

	// Create temporary file and open editor
	editedContent, err := o.editInEditor(originalContent, kind, name)
	if err != nil {
		return fmt.Errorf("editing resource: %w", err)
	}

	// Apply the changes
	applyCtx, applyCancel := o.WithTimeout(ctx)
	err = o.applyChanges(applyCtx, clientWithResponses, editedContent, kind, name, originalResource)
	applyCancel()
	if err != nil {
		// Save the edited content to a temporary file for recovery
		tempFile, saveErr := o.saveToTempFile(editedContent, kind, name)
		if saveErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save edited content to temporary file: %v\n", saveErr)
		} else {
			fmt.Fprintf(os.Stderr, "Your changes have been saved to: %s\n", tempFile)
			fmt.Fprintf(os.Stderr, "You can apply these changes later using: flightctl apply -f %s\n", tempFile)
		}
		return fmt.Errorf("applying changes: %w", err)
	}

	fmt.Printf("%s/%s edited\n", kind, name)
	return nil
}

func (o *EditOptions) fetchResource(ctx context.Context, c *apiclient.ClientWithResponses, kind, name string) (interface{}, error) {
	switch kind {
	case DeviceKind:
		return c.GetDeviceWithResponse(ctx, name)
	case EnrollmentRequestKind:
		return c.GetEnrollmentRequestWithResponse(ctx, name)
	case FleetKind:
		params := api.GetFleetParams{}
		return c.GetFleetWithResponse(ctx, name, &params)
	case TemplateVersionKind:
		return c.GetTemplateVersionWithResponse(ctx, o.FleetName, name)
	case RepositoryKind:
		return c.GetRepositoryWithResponse(ctx, name)
	case ResourceSyncKind:
		return c.GetResourceSyncWithResponse(ctx, name)
	case CertificateSigningRequestKind:
		return c.GetCertificateSigningRequestWithResponse(ctx, name)
	default:
		return nil, fmt.Errorf("unsupported resource kind: %s", kind)
	}
}

func (o *EditOptions) resourceToFormat(response interface{}) ([]byte, error) {
	// Validate the response
	if err := validateResponse(response); err != nil {
		return nil, err
	}

	// Extract JSON200 data
	json200, err := responseField[interface{}](response, "JSON200")
	if err != nil {
		return nil, err
	}

	// Convert to the specified format
	switch o.Output {
	case "yaml":
		yamlBytes, err := yaml.Marshal(json200)
		if err != nil {
			return nil, fmt.Errorf("marshaling to YAML: %w", err)
		}
		return yamlBytes, nil
	case "json":
		jsonBytes, err := json.MarshalIndent(json200, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshaling to JSON: %w", err)
		}
		return jsonBytes, nil
	default:
		return nil, fmt.Errorf("unsupported output format: %s", o.Output)
	}
}

func (o *EditOptions) editInEditor(content []byte, kind, name string) ([]byte, error) {
	// Create temporary file with appropriate extension
	extension := o.Output
	tempFile, err := os.CreateTemp("", fmt.Sprintf("flightctl-edit-%s-%s-*.%s", kind, name, extension))
	if err != nil {
		return nil, fmt.Errorf("creating temporary file: %w", err)
	}
	defer os.Remove(tempFile.Name())

	// Write content to temp file
	if _, err := tempFile.Write(content); err != nil {
		tempFile.Close()
		return nil, fmt.Errorf("writing to temporary file: %w", err)
	}
	tempFile.Close()

	// Open editor with appropriate flags
	var editorCmd string
	if strings.Contains(strings.ToLower(o.Editor), "nano") {
		// For nano, use flags to avoid format prompts and make it more user-friendly
		// -N: don't convert from DOS/Mac format, -u: save in Unix format
		editorCmd = fmt.Sprintf("%s -N -u %s", o.Editor, tempFile.Name())
	} else {
		editorCmd = fmt.Sprintf("%s %s", o.Editor, tempFile.Name())
	}
	cmd := exec.Command("sh", "-c", editorCmd)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("running editor: %w", err)
	}

	// Read edited content
	editedContent, err := os.ReadFile(tempFile.Name())
	if err != nil {
		return nil, fmt.Errorf("reading edited file: %w", err)
	}

	return editedContent, nil
}

// calculateJSONPatch calculates the JSON patch between original and edited resources
func (o *EditOptions) calculateJSONPatch(originalResource, editedResource interface{}) ([]byte, error) {
	originalMap, ok := originalResource.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("original resource is not a map, got type: %T", originalResource)
	}

	editedMap, ok := editedResource.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("edited resource is not a map")
	}

	// Ensure critical fields are preserved (name, kind, apiVersion should not change)
	originalMetadata, _ := originalMap["metadata"].(map[string]interface{})
	editedMetadata, _ := editedMap["metadata"].(map[string]interface{})

	// Verify critical fields are preserved
	if originalMetadata["name"] != editedMetadata["name"] {
		return nil, fmt.Errorf("metadata.name cannot be changed during edit")
	}
	if originalMap["kind"] != editedMap["kind"] {
		return nil, fmt.Errorf("kind cannot be changed during edit")
	}
	if originalMap["apiVersion"] != editedMap["apiVersion"] {
		return nil, fmt.Errorf("apiVersion cannot be changed during edit")
	}

	// Create a comprehensive patch that includes all changes except the protected fields
	var patch []map[string]interface{}

	// Patch spec - use "add" if original has no spec, "replace" if it does
	originalSpec, originalHasSpec := originalMap["spec"]
	editedSpec, editedHasSpec := editedMap["spec"]

	if editedHasSpec {
		// Only add spec operation if values are different
		if !originalHasSpec || !reflect.DeepEqual(originalSpec, editedSpec) {
			if originalHasSpec {
				// Use replace if original has spec
				patch = append(patch, map[string]interface{}{
					"op":    "replace",
					"path":  "/spec",
					"value": editedSpec,
				})
			} else {
				// Use add if original has no spec
				patch = append(patch, map[string]interface{}{
					"op":    "add",
					"path":  "/spec",
					"value": editedSpec,
				})
			}
		}
	}

	// Patch metadata fields individually (excluding name which is protected)
	if editedMetadata != nil {
		originalLabels, originalHasLabels := originalMetadata["labels"]
		editedLabels, editedHasLabels := editedMetadata["labels"]

		// Check for changes in labels
		if editedHasLabels {
			// Only add labels operation if values are different
			if !originalHasLabels || !reflect.DeepEqual(originalLabels, editedLabels) {
				if originalHasLabels {
					// Use replace if original has labels
					patch = append(patch, map[string]interface{}{
						"op":    "replace",
						"path":  "/metadata/labels",
						"value": editedLabels,
					})
				} else {
					// Use add if original has no labels
					patch = append(patch, map[string]interface{}{
						"op":    "add",
						"path":  "/metadata/labels",
						"value": editedLabels,
					})
				}
			}
		}

		originalAnnotations, originalHasAnnotations := originalMetadata["annotations"]
		editedAnnotations, editedHasAnnotations := editedMetadata["annotations"]

		// Check for changes in annotations
		if editedHasAnnotations {
			// Only add annotations operation if values are different
			if !originalHasAnnotations || !reflect.DeepEqual(originalAnnotations, editedAnnotations) {
				if originalHasAnnotations {
					// Use replace if original has annotations
					patch = append(patch, map[string]interface{}{
						"op":    "replace",
						"path":  "/metadata/annotations",
						"value": editedAnnotations,
					})
				} else {
					// Use add if original has no annotations
					patch = append(patch, map[string]interface{}{
						"op":    "add",
						"path":  "/metadata/annotations",
						"value": editedAnnotations,
					})
				}
			}
		}
	}

	// If no changes, return empty patch
	if len(patch) == 0 {
		return []byte("[]"), nil
	}

	// Convert patch to JSON
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("marshaling JSON patch: %w", err)
	}

	return patchJSON, nil
}

func (o *EditOptions) applyChanges(ctx context.Context, client *apiclient.ClientWithResponses, content []byte, kind, name string, originalResource interface{}) error {
	// Parse the content based on the format
	var resource map[string]interface{}
	var err error

	switch o.Output {
	case "yaml":
		err = yaml.Unmarshal(content, &resource)
		if err != nil {
			return fmt.Errorf("parsing edited YAML: %w", err)
		}
	case "json":
		err = json.Unmarshal(content, &resource)
		if err != nil {
			return fmt.Errorf("parsing edited JSON: %w", err)
		}
	default:
		return fmt.Errorf("unsupported format for parsing: %s", o.Output)
	}

	// Validate the resource structure and preserve required fields
	resourceKind, ok := resource["kind"].(string)
	if !ok {
		return fmt.Errorf("edited resource missing 'kind' field")
	}
	if strings.ToLower(resourceKind) != kind {
		return fmt.Errorf("cannot change resource kind from %s to %s", kind, resourceKind)
	}

	metadata, ok := resource["metadata"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("edited resource missing 'metadata' field")
	}
	resourceName, ok := metadata["name"].(string)
	if !ok {
		return fmt.Errorf("edited resource missing 'metadata.name' field")
	}
	if resourceName != name {
		return fmt.Errorf("cannot change resource name from %s to %s", name, resourceName)
	}

	// Extract the original resource data for patch calculation
	originalJSON200, err := responseField[interface{}](originalResource, "JSON200")
	if err != nil {
		return fmt.Errorf("extracting original resource data: %w", err)
	}

	// Convert the original resource to a map[string]interface{} for patch calculation
	originalJSONBytes, err := json.Marshal(originalJSON200)
	if err != nil {
		return fmt.Errorf("marshaling original resource to JSON: %w", err)
	}

	var originalMap map[string]interface{}
	if err := json.Unmarshal(originalJSONBytes, &originalMap); err != nil {
		return fmt.Errorf("unmarshaling original resource to map: %w", err)
	}

	// Calculate JSON patch
	patchJSON, err := o.calculateJSONPatch(originalMap, resource)
	if err != nil {
		return fmt.Errorf("calculating JSON patch: %w", err)
	}

	// If no changes, nothing to do (check if patch is empty "[]" or zero-length)
	if len(patchJSON) == 0 || string(patchJSON) == "[]" {
		fmt.Printf("%s/%s unchanged\n", kind, name)
		return nil
	}

	// Apply changes using PATCH operation
	var httpResponse *http.Response
	var responseErr error

	switch kind {
	case DeviceKind:
		response, err := client.PatchDeviceWithBodyWithResponse(ctx, name, "application/json-patch+json", bytes.NewReader(patchJSON))
		if response != nil {
			httpResponse = response.HTTPResponse
		}
		responseErr = err
	case FleetKind:
		response, err := client.PatchFleetWithBodyWithResponse(ctx, name, "application/json-patch+json", bytes.NewReader(patchJSON))
		if response != nil {
			httpResponse = response.HTTPResponse
		}
		responseErr = err
	case RepositoryKind:
		response, err := client.PatchRepositoryWithBodyWithResponse(ctx, name, "application/json-patch+json", bytes.NewReader(patchJSON))
		if response != nil {
			httpResponse = response.HTTPResponse
		}
		responseErr = err
	default:
		return fmt.Errorf("unsupported resource kind for editing: %s (PATCH not supported)", kind)
	}

	if responseErr != nil {
		return responseErr
	}

	if httpResponse != nil && httpResponse.StatusCode != http.StatusOK && httpResponse.StatusCode != http.StatusCreated && httpResponse.StatusCode != http.StatusNoContent {
		return fmt.Errorf("server returned status: %s", httpResponse.Status)
	}

	return nil
}

func (o *EditOptions) saveToTempFile(content []byte, kind, name string) (string, error) {
	extension := o.Output
	tempFile, err := os.CreateTemp("", fmt.Sprintf("flightctl-edit-failed-%s-%s-*.%s", kind, name, extension))
	if err != nil {
		return "", err
	}
	defer tempFile.Close()

	if _, err := tempFile.Write(content); err != nil {
		os.Remove(tempFile.Name())
		return "", err
	}

	return tempFile.Name(), nil
}
