package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type DownloadOptions struct {
	GlobalOptions
	Output string
}

// downloadResult holds the successful download stream and metadata
type downloadResult struct {
	reader    io.Reader
	closeFunc func() error
	totalSize int64
}

func DefaultDownloadOptions() *DownloadOptions {
	return &DownloadOptions{
		GlobalOptions: DefaultGlobalOptions(),
		Output:        "",
	}
}

func NewCmdDownload() *cobra.Command {
	o := DefaultDownloadOptions()
	cmd := &cobra.Command{
		Use:     "download TYPE/NAME OUTPUT_FILE",
		Short:   "Download a resource artifact",
		Long:    "Download a resource artifact. Currently supports imageexport resources.",
		Args:    cobra.ExactArgs(2),
		Example: "  flightctl download imageexport/my-export ./artifact.qcow2",
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

func (o *DownloadOptions) Bind(fs *pflag.FlagSet) {
	o.GlobalOptions.Bind(fs)
}

func (o *DownloadOptions) Complete(cmd *cobra.Command, args []string) error {
	if err := o.GlobalOptions.Complete(cmd, args); err != nil {
		return err
	}

	// Set output file from second argument
	if len(args) >= 2 {
		o.Output = args[1]
	}

	return nil
}

func (o *DownloadOptions) Validate(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("resource type/name is required")
	}

	// Parse resource type/name
	parts := strings.Split(args[0], "/")
	if len(parts) != 2 {
		return fmt.Errorf("resource must be in format TYPE/NAME (e.g., imageexport/my-export)")
	}

	kind, err := ResourceKindFromString(parts[0])
	if err != nil {
		return fmt.Errorf("invalid resource type: %s", parts[0])
	}

	if kind != ImageExportKind {
		return fmt.Errorf("unsupported resource type: %s (only 'imageexport' is supported)", kind)
	}

	// Validate output file
	if o.Output == "" {
		return fmt.Errorf("output file path is required")
	}

	return nil
}

func (o *DownloadOptions) Run(ctx context.Context, args []string) error {
	// Parse resource type/name
	parts := strings.Split(args[0], "/")
	kind, err := ResourceKindFromString(parts[0])
	if err != nil {
		return fmt.Errorf("invalid resource type: %s", parts[0])
	}
	name := parts[1]

	// Check if file exists and prompt for overwrite before starting download
	if err := o.checkFileExists(); err != nil {
		return err
	}

	var result *downloadResult

	switch kind {
	case ImageExportKind:
		result, err = o.downloadImageExport(ctx, name)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported resource type: %s", kind)
	}

	if result.closeFunc != nil {
		defer func() {
			if err := result.closeFunc(); err != nil {
				// Log the error but don't fail the operation if saveToFile already succeeded
				fmt.Fprintf(os.Stderr, "Warning: failed to close download stream: %v\n", err)
			}
		}()
	}

	// Save stream to file with progress
	return o.saveToFile(result.reader, result.totalSize)
}

func (o *DownloadOptions) downloadImageExport(ctx context.Context, name string) (*downloadResult, error) {
	// Build client - let it follow redirects normally since we're streaming
	ibClient, err := o.BuildImageBuilderClient()
	if err != nil {
		return nil, fmt.Errorf("creating imagebuilder client: %w", err)
	}

	// Use raw DownloadImageExport to get *http.Response without reading body
	// The HTTP client will automatically follow redirects to the presigned URL
	httpResp, err := ibClient.DownloadImageExport(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to download imageexport: %w", err)
	}

	// Handle error responses
	if httpResp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		return nil, validateHttpResponse(bodyBytes, httpResp.StatusCode, http.StatusOK)
	}

	// Handle successful download (200 or after redirect) - stream directly from response body
	// Try to get Content-Length from headers
	var totalSize int64
	if contentLength := httpResp.Header.Get("Content-Length"); contentLength != "" {
		if size, err := strconv.ParseInt(contentLength, 10, 64); err == nil {
			totalSize = size
		}
	}
	// Return the response body directly as a reader - caller will close it via closeFunc
	return &downloadResult{
		reader:    httpResp.Body,
		closeFunc: httpResp.Body.Close,
		totalSize: totalSize,
	}, nil
}

// progressWriter wraps an io.Writer and reports progress as data is written
type progressWriter struct {
	writer       io.Writer
	totalBytes   int64
	bytesWritten int64
	lastUpdate   time.Time
	outputPath   string
}

func newProgressWriter(writer io.Writer, totalBytes int64, outputPath string) *progressWriter {
	return &progressWriter{
		writer:     writer,
		totalBytes: totalBytes,
		outputPath: outputPath,
		lastUpdate: time.Now(),
	}
}

func (pw *progressWriter) Write(p []byte) (n int, err error) {
	n, err = pw.writer.Write(p)
	pw.bytesWritten += int64(n)

	// Update progress every 100ms or when complete
	now := time.Now()
	if now.Sub(pw.lastUpdate) >= 100*time.Millisecond || (pw.totalBytes > 0 && pw.bytesWritten >= pw.totalBytes) {
		pw.printProgress()
		pw.lastUpdate = now
	}

	return n, err
}

func (pw *progressWriter) printProgress() {
	// Use ANSI escape code to clear the line, then print progress
	// \033[2K clears the entire line, \r moves cursor to beginning
	if pw.totalBytes > 0 {
		percent := float64(pw.bytesWritten) / float64(pw.totalBytes) * 100
		fmt.Fprintf(os.Stderr, "\r\033[2KDownloading: %s/%s (%.1f%%)", formatBytes(pw.bytesWritten), formatBytes(pw.totalBytes), percent)
	} else {
		fmt.Fprintf(os.Stderr, "\r\033[2KDownloading: %s", formatBytes(pw.bytesWritten))
	}
}

func (pw *progressWriter) finish() {
	// Clear line and print final message
	if pw.totalBytes > 0 {
		fmt.Fprintf(os.Stderr, "\r\033[2KDownloaded: %s/%s (100.0%%)\n", formatBytes(pw.totalBytes), formatBytes(pw.totalBytes))
	} else {
		fmt.Fprintf(os.Stderr, "\r\033[2KDownloaded: %s\n", formatBytes(pw.bytesWritten))
	}
	fmt.Fprintf(os.Stderr, "Saved to: %s\n", pw.outputPath)
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func (o *DownloadOptions) checkFileExists() error {
	// Check if file already exists
	if _, err := os.Stat(o.Output); err == nil {
		// File exists, prompt user
		fmt.Fprintf(os.Stderr, "File %s already exists. Overwrite? (y/n): ", o.Output)
		reader := bufio.NewReader(os.Stdin)
		response, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read user input: %w", err)
		}
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			return fmt.Errorf("download cancelled")
		}
	}
	return nil
}

func (o *DownloadOptions) saveToFile(reader io.Reader, totalSize int64) error {
	// Create output file
	outFile, err := os.Create(o.Output)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	// Create progress writer
	progressWriter := newProgressWriter(outFile, totalSize, o.Output)

	// Copy stream to file with progress tracking
	_, err = io.Copy(progressWriter, reader)
	if err != nil {
		return fmt.Errorf("failed to write to output file: %w", err)
	}

	// Only call finish() on successful copy
	progressWriter.finish()

	return nil
}
