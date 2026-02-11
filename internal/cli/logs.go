package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	imagebuilderapi "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type LogsOptions struct {
	GlobalOptions
	Follow bool
}

func DefaultLogsOptions() *LogsOptions {
	return &LogsOptions{
		GlobalOptions: DefaultGlobalOptions(),
		Follow:        false,
	}
}

func NewCmdLogs() *cobra.Command {
	o := DefaultLogsOptions()
	cmd := &cobra.Command{
		Use:   "logs (TYPE/NAME | TYPE NAME) [flags]",
		Short: "Print the logs for a resource",
		Long:  "Print the logs for a resource. Supports imagebuild and imageexport resources.",
		Example: `  # Get logs for an imagebuild
  flightctl logs imagebuild/my-build

  # Follow logs for an active imagebuild
  flightctl logs imagebuild/my-build -f

  # Get logs for an imageexport
  flightctl logs imageexport/my-export

  # Follow logs for an active imageexport
  flightctl logs imageexport/my-export -f`,
		Args: cobra.RangeArgs(1, 2),
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

func (o *LogsOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
	fs.BoolVarP(&o.Follow, "follow", "f", o.Follow, "Specify if the logs should be streamed. Follows the logs until the build completes or the command is interrupted.")
}

func (o *LogsOptions) Complete(cmd *cobra.Command, args []string) error {
	return o.GlobalOptions.Complete(cmd, args)
}

func (o *LogsOptions) Validate(args []string) error {
	if err := o.GlobalOptions.Validate(args); err != nil {
		return err
	}

	// Parse the resource argument(s)
	kind, name, err := parseResourceArgs(args)
	if err != nil {
		return err
	}

	// Support imagebuild and imageexport
	if kind != ImageBuildKind && kind != ImageExportKind {
		return fmt.Errorf("logs command only supports imagebuild and imageexport resources, got: %s", kind)
	}

	if name == "" {
		return fmt.Errorf("resource name is required")
	}

	return nil
}

func (o *LogsOptions) Run(ctx context.Context, args []string) error {
	kind, name, err := parseResourceArgs(args)
	if err != nil {
		return err
	}

	// Build imagebuilder client
	ibClient, err := o.BuildImageBuilderClient()
	if err != nil {
		return fmt.Errorf("creating imagebuilder client: %w", err)
	}

	// Prepare follow parameter
	var follow *bool
	if o.Follow {
		f := true
		follow = &f
	}

	// Make the request based on resource kind
	// Use the raw HTTP response methods (not WithResponse) to allow streaming
	var resp *http.Response
	switch kind {
	case ImageBuildKind:
		params := &imagebuilderapi.GetImageBuildLogParams{Follow: follow}
		resp, err = ibClient.GetImageBuildLog(ctx, name, params)
	case ImageExportKind:
		params := &imagebuilderapi.GetImageExportLogParams{Follow: follow}
		resp, err = ibClient.GetImageExportLog(ctx, name, params)
	default:
		return fmt.Errorf("unsupported resource kind for logs: %s", kind)
	}

	if err != nil {
		return fmt.Errorf("requesting logs: %w", err)
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return validateHttpResponse(body, resp.StatusCode, http.StatusOK)
	}

	// Handle streaming vs non-streaming
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") {
		// SSE streaming
		return o.handleSSEStream(resp.Body)
	} else {
		// Plain text
		_, err := io.Copy(os.Stdout, resp.Body)
		return err
	}
}

// handleSSEStream processes Server-Sent Events stream
// Returns nil on orderly close (completion marker received) or error on abrupt close
func (o *LogsOptions) handleSSEStream(body io.Reader) error {
	scanner := bufio.NewScanner(body)
	// Increase buffer size to handle very long log lines (default 64KB is often insufficient)
	const maxScannerBuffer = 1024 * 1024 // 1MB
	scanner.Buffer(make([]byte, 0, maxScannerBuffer), maxScannerBuffer)
	var currentData strings.Builder
	streamCompleted := false

	for scanner.Scan() {
		line := scanner.Text()

		// SSE format: "data: {content}\n\n"
		if strings.HasPrefix(line, "data: ") {
			// Extract the data after "data: "
			data := strings.TrimPrefix(line, "data: ")
			// Check for completion marker - indicates orderly stream close
			if data == imagebuilderapi.LogStreamCompleteMarker {
				streamCompleted = true
				break
			}
			currentData.WriteString(data)
		} else if line == "" {
			// Empty line after data line - this is the SSE delimiter
			// Output accumulated data and reset
			if currentData.Len() > 0 {
				data := currentData.String()
				// Ensure data ends with newline if it doesn't already
				if !strings.HasSuffix(data, "\n") {
					data += "\n"
				}
				fmt.Print(data)
				currentData.Reset()
			}
		} else {
			// Regular log line (not SSE formatted) - output directly with newline
			fmt.Println(line)
		}
	}

	// Output any remaining data
	if currentData.Len() > 0 {
		data := currentData.String()
		// Ensure data ends with newline if it doesn't already
		if !strings.HasSuffix(data, "\n") {
			data += "\n"
		}
		fmt.Print(data)
	}

	if err := scanner.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			// User cancelled (Ctrl+C) - not an error
			return nil
		}
		return fmt.Errorf("reading stream: %w", err)
	}

	// Check if stream ended orderly (with completion marker) or abruptly
	if !streamCompleted {
		return fmt.Errorf("stream closed unexpectedly (connection lost or server error)")
	}

	return nil
}

// parseResourceArgs parses resource arguments supporting both "type/name" (single arg) and "type name" (two args) formats
func parseResourceArgs(args []string) (ResourceKind, string, error) {
	if len(args) == 2 {
		// Two separate args: "type" "name"
		kind, err := ResourceKindFromString(args[0])
		if err != nil {
			return InvalidKind, "", fmt.Errorf("invalid resource kind: %w", err)
		}
		return kind, args[1], nil
	}

	// Single arg: try "type/name" format
	parts := strings.SplitN(args[0], "/", 2)
	if len(parts) == 2 {
		kind, err := ResourceKindFromString(parts[0])
		if err != nil {
			return InvalidKind, "", fmt.Errorf("invalid resource kind: %w", err)
		}
		return kind, parts[1], nil
	}

	return InvalidKind, "", fmt.Errorf("invalid resource format: expected 'type/name' or 'type name', got: %s", args[0])
}
