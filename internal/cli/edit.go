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

	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/cli/display"
	jsonpatchcreate "github.com/mattbaird/jsonpatch"
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
		Output:        string(display.YAMLFormat),
	}
}

// getValidEditResourceKinds returns the resource kinds that support editing
func getValidEditResourceKinds() []ResourceKind {
	return []ResourceKind{
		DeviceKind,
		FleetKind,
		RepositoryKind,
		CertificateSigningRequestKind,
	}
}

type errEditNotAllowed struct {
	ResourceKind
}

func (e errEditNotAllowed) Error() string {
	return "edit not permitted for " + e.ResourceKind.String()
}

func NewCmdEdit() *cobra.Command {
	o := DefaultEditOptions()
	cmd := &cobra.Command{
		Use:   "edit (TYPE NAME | TYPE/NAME)",
		Short: "Edit a resource in your default editor.",
		Long: `Edit a resource from your default command-line editor.

The edit command allows you to directly edit an API resource. It opens the editor defined by --editor,
then FLIGHTCTL_EDITOR, then VISUAL, then EDITOR environment variables, or falls back to 'vi'.

Edits are applied using JSON patch operations for efficiency.

Editing uses the API version used to fetch the resource.

The default format is YAML. To edit in JSON, specify "-o json".

In the event an error occurs while updating, a temporary file will be created on disk
that contains your unapplied changes. The most common error when updating a resource
is another editor changing the resource on the server. When this occurs, you will have
to apply your changes to the newer version of the resource, or update your temporary
saved copy to include the latest resource version.`,
		Args: cobra.RangeArgs(1, 2),
		ValidArgsFunction: KindNameAutocomplete{
			Options:            o,
			AllowMultipleNames: false,
			AllowedKinds:       getValidEditResourceKinds(),
		}.ValidArgsFunction,
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
	fs.StringVar(&o.Editor, "editor", o.Editor, "Editor to use for editing the resource. If not specified, uses FLIGHTCTL_EDITOR, then VISUAL, then EDITOR environment variables, or falls back to 'vi'.")
	fs.StringVarP(&o.Output, "output", "o", o.Output, fmt.Sprintf("Output format. One of: (%s, %s). Default: %s", display.YAMLFormat, display.JSONFormat, display.YAMLFormat))
}

func (o *EditOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}

	// Determine the editor to use
	if o.Editor == "" {
		if editor := os.Getenv("FLIGHTCTL_EDITOR"); editor != "" {
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

	// Check if resource type supports editing
	switch kind {
	case DeviceKind, FleetKind, RepositoryKind, CertificateSigningRequestKind, AuthProviderKind:
		// These are supported for editing
	default:
		return errEditNotAllowed{kind}
	}

	// Validate output format
	if o.Output != string(display.YAMLFormat) && o.Output != string(display.JSONFormat) {
		return fmt.Errorf("output format must be one of (%s, %s), got: %s", display.YAMLFormat, display.JSONFormat, o.Output)
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
	var originalResource interface{}

	switch kind {
	case TemplateVersionKind:
		originalResource, err = GetTemplateVersion(fetchCtx, clientWithResponses, o.FleetName, name)
	default:
		originalResource, err = GetSingleResource(fetchCtx, clientWithResponses, kind, name)
	}
	fetchCancel()
	if err != nil {
		return fmt.Errorf("fetching %s/%s: %w", kind, name, err)
	}

	// Convert to the specified format for editing
	originalContent, err := o.resourceToFormat(originalResource, o.Output)
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

func (o *EditOptions) editInEditor(content []byte, kind ResourceKind, name string) ([]byte, error) {
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
	// 1. Convert the resources to JSON bytes for the library
	originalJSON, err := json.Marshal(originalResource)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal original resource: %w", err)
	}

	editedJSON, err := json.Marshal(editedResource)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal edited resource: %w", err)
	}

	// 2. Generate a complete, fine-grained patch using the mattbaird/jsonpatch library
	patch, err := jsonpatchcreate.CreatePatch(originalJSON, editedJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to create JSON patch: %w", err)
	}

	// 3. Filter the patch to protect immutable fields
	var filteredOps []jsonpatchcreate.JsonPatchOperation

	// Define paths that cannot be changed
	protectedPaths := []string{
		"/metadata/name",
		"/kind",
		"/apiVersion",
	}

	// 4. Add a test operation for resourceVersion FIRST to ensure conflict detection
	// Only add the test if the patch doesn't contain a remove operation for resourceVersion
	originalMap, ok := originalResource.(map[string]interface{})
	if ok {
		if metadata, ok := originalMap["metadata"].(map[string]interface{}); ok {
			if resourceVersion, ok := metadata["resourceVersion"].(string); ok {
				// Check if the patch contains a remove operation for resourceVersion or metadata
				hasRemoveResourceVersion := false
				for _, op := range patch {
					if op.Operation == "remove" &&
						(op.Path == "/metadata/resourceVersion" || op.Path == "/metadata") {
						hasRemoveResourceVersion = true
						break
					}
				}

				// Only add test operation if patch doesn't remove resourceVersion
				if !hasRemoveResourceVersion {
					testOp := jsonpatchcreate.JsonPatchOperation{
						Operation: "test",
						Path:      "/metadata/resourceVersion",
						Value:     resourceVersion,
					}
					filteredOps = append(filteredOps, testOp)
				}
			}
		}
	}

	// Filter out operations that modify protected fields
	for _, op := range patch {
		path := op.Path
		isProtected := false

		for _, protected := range protectedPaths {
			// Check if the operation's path matches or is a sub-path of a protected one
			if strings.HasPrefix(path, protected) {
				isProtected = true
				break
			}
		}

		if !isProtected {
			filteredOps = append(filteredOps, op)
		}
	}

	// If no operations were generated, return an empty patch
	if len(filteredOps) == 0 {
		return []byte("[]"), nil
	}

	// 5. Marshal the final, filtered patch back to JSON
	return json.Marshal(filteredOps)
}

func (o *EditOptions) applyChanges(ctx context.Context, client *apiclient.ClientWithResponses, content []byte, kind ResourceKind, name string, originalResource interface{}) error {
	// Parse and validate the edited content
	resource, err := o.parseEditedContent(content)
	if err != nil {
		return err
	}

	if err := o.validateEditedResource(resource, kind, name); err != nil {
		return err
	}

	// Calculate and apply the patch
	patchJSON, err := o.calculatePatch(originalResource, resource)
	if err != nil {
		return err
	}

	if o.isPatchEmpty(patchJSON) {
		fmt.Printf("%s/%s unchanged\n", kind, name)
		return nil
	}

	return o.applyPatch(ctx, client, kind, name, patchJSON)
}

// parseEditedContent parses the edited content based on the output format
func (o *EditOptions) parseEditedContent(content []byte) (map[string]interface{}, error) {
	var resource map[string]interface{}
	var err error

	switch o.Output {
	case string(display.YAMLFormat):
		err = yaml.Unmarshal(content, &resource)
		if err != nil {
			return nil, fmt.Errorf("parsing edited YAML: %w", err)
		}
	case string(display.JSONFormat):
		err = json.Unmarshal(content, &resource)
		if err != nil {
			return nil, fmt.Errorf("parsing edited JSON: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported format for parsing: %s", o.Output)
	}

	return resource, nil
}

// validateEditedResource validates the edited resource structure and required fields
func (o *EditOptions) validateEditedResource(resource map[string]interface{}, kind ResourceKind, name string) error {
	resourceKind, ok := resource["kind"].(string)
	if !ok {
		return fmt.Errorf("edited resource missing 'kind' field")
	}
	if strings.ToLower(resourceKind) != kind.String() {
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

	return nil
}

// calculatePatch calculates the JSON patch between original and edited resources
func (o *EditOptions) calculatePatch(originalResource interface{}, editedResource map[string]interface{}) ([]byte, error) {
	// Extract the original resource data for patch calculation
	originalJSON200, err := responseField[interface{}](originalResource, "JSON200")
	if err != nil {
		return nil, fmt.Errorf("extracting original resource data: %w", err)
	}

	// Convert the original resource to a map[string]interface{} for patch calculation
	originalJSONBytes, err := json.Marshal(originalJSON200)
	if err != nil {
		return nil, fmt.Errorf("marshaling original resource to JSON: %w", err)
	}

	var originalMap map[string]interface{}
	if err := json.Unmarshal(originalJSONBytes, &originalMap); err != nil {
		return nil, fmt.Errorf("unmarshaling original resource to map: %w", err)
	}

	// Calculate JSON patch
	patchJSON, err := o.calculateJSONPatch(originalMap, editedResource)
	if err != nil {
		return nil, fmt.Errorf("calculating JSON patch: %w", err)
	}

	return patchJSON, nil
}

// isPatchEmpty checks if the patch is empty or contains no changes
func (o *EditOptions) isPatchEmpty(patchJSON []byte) bool {
	return len(patchJSON) == 0 || string(patchJSON) == "[]"
}

// applyPatch applies the JSON patch to the resource using the appropriate PATCH operation
func (o *EditOptions) applyPatch(ctx context.Context, client *apiclient.ClientWithResponses, kind ResourceKind, name string, patchJSON []byte) error {
	httpResponse, responseBody, err := o.executePatchOperation(ctx, client, kind, name, patchJSON)
	if err != nil {
		return err
	}

	return o.handlePatchResponse(httpResponse, responseBody)
}

// executePatchOperation executes the appropriate PATCH operation based on resource kind
func (o *EditOptions) executePatchOperation(ctx context.Context, client *apiclient.ClientWithResponses, kind ResourceKind, name string, patchJSON []byte) (*http.Response, []byte, error) {
	reader := bytes.NewReader(patchJSON)
	contentType := "application/json-patch+json"

	switch kind {
	case DeviceKind:
		response, err := client.PatchDeviceWithBodyWithResponse(ctx, name, contentType, reader)
		return o.extractResponseData(response, err)
	case FleetKind:
		response, err := client.PatchFleetWithBodyWithResponse(ctx, name, contentType, reader)
		return o.extractResponseData(response, err)
	case RepositoryKind:
		response, err := client.PatchRepositoryWithBodyWithResponse(ctx, name, contentType, reader)
		return o.extractResponseData(response, err)
	case CertificateSigningRequestKind:
		response, err := client.PatchCertificateSigningRequestWithBodyWithResponse(ctx, name, contentType, reader)
		return o.extractResponseData(response, err)
	case AuthProviderKind:
		response, err := client.PatchAuthProviderWithBodyWithResponse(ctx, name, contentType, reader)
		return o.extractResponseData(response, err)
	case EnrollmentRequestKind:
		response, err := client.PatchEnrollmentRequestWithBodyWithResponse(ctx, name, contentType, reader)
		return o.extractResponseData(response, err)
	default:
		return nil, nil, fmt.Errorf("unsupported resource kind for editing: %s (PATCH not supported)", kind)
	}
}

// extractResponseData extracts HTTP response data from the API response
func (o *EditOptions) extractResponseData(response interface{}, err error) (*http.Response, []byte, error) {
	if err != nil {
		return nil, nil, err
	}

	// Use reflection to extract response data (since different APIs return different types)
	v := reflect.ValueOf(response)
	if v.Kind() == reflect.Ptr && !v.IsNil() {
		v = v.Elem()
	}

	var httpResponse *http.Response
	var responseBody []byte

	if httpField := v.FieldByName("HTTPResponse"); httpField.IsValid() && !httpField.IsNil() {
		httpResponse = httpField.Interface().(*http.Response)
	}

	if bodyField := v.FieldByName("Body"); bodyField.IsValid() {
		responseBody = bodyField.Interface().([]byte)
	}

	return httpResponse, responseBody, nil
}

// handlePatchResponse handles the response from the PATCH operation
func (o *EditOptions) handlePatchResponse(httpResponse *http.Response, responseBody []byte) error {
	if httpResponse == nil {
		return nil
	}

	if httpResponse.StatusCode == http.StatusOK || httpResponse.StatusCode == http.StatusCreated || httpResponse.StatusCode == http.StatusNoContent {
		return nil
	}

	// Handle error response
	if len(responseBody) > 0 {
		return fmt.Errorf("server returned status: %s\nError details: %s", httpResponse.Status, string(responseBody))
	}
	return fmt.Errorf("server returned status: %s", httpResponse.Status)
}

func (o *EditOptions) saveToTempFile(content []byte, kind ResourceKind, name string) (string, error) {
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

// resourceToFormat converts a resource response to the specified format (yaml or json).
func (o *EditOptions) resourceToFormat(response interface{}, outputFormat string) ([]byte, error) {
	// Extract JSON200 data
	json200, err := ExtractJSON200(response)
	if err != nil {
		return nil, err
	}

	// Convert to the specified format
	switch outputFormat {
	case string(display.YAMLFormat):
		yamlBytes, err := yaml.Marshal(json200)
		if err != nil {
			return nil, fmt.Errorf("marshaling to YAML: %w", err)
		}
		return yamlBytes, nil
	case string(display.JSONFormat):
		jsonBytes, err := json.MarshalIndent(json200, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshaling to JSON: %w", err)
		}
		return jsonBytes, nil
	default:
		return nil, fmt.Errorf("unsupported output format: %s", outputFormat)
	}
}
