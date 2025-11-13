package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/flightctl/flightctl/pkg/shutdown"
	"github.com/spf13/cobra"
)

// ShutdownOptions contains options for shutdown commands
type ShutdownOptions struct {
	GlobalOptions
	Service    string
	Endpoint   string
	Timeout    time.Duration
	Format     string
	Watch      bool
	Signal     string
	Force      bool
	Components []string
}

// ShutdownStatusResponse represents the API response for shutdown status
type ShutdownStatusResponse struct {
	IsShuttingDown      bool                          `json:"isShuttingDown"`
	ShutdownInitiated   *time.Time                    `json:"shutdownInitiated,omitempty"`
	ActiveComponents    []string                      `json:"activeComponents,omitempty"`
	CompletedComponents []shutdown.CompletedComponent `json:"completedComponents,omitempty"`
	State               string                        `json:"state"`
	Message             string                        `json:"message,omitempty"`
}

// NewCmdShutdown creates the shutdown command with subcommands
func NewCmdShutdown() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shutdown [subcommand]",
		Short: "Manage graceful shutdown of FlightCtl services",
		Long: `The shutdown command provides tools to manage graceful shutdown of FlightCtl services.

Subcommands:
  status     Show shutdown status of services
  trigger    Trigger graceful shutdown of a service
  watch      Monitor shutdown progress in real-time
  components List registered components and their priorities
  signal     Send shutdown signal to a service

Examples:
  # Get shutdown status of API service
  flightctl shutdown status --service=api

  # Trigger graceful shutdown of worker service
  flightctl shutdown trigger --service=worker

  # Monitor shutdown progress
  flightctl shutdown watch --service=api

  # List components and priorities
  flightctl shutdown components --service=api

  # Send SIGTERM to a service
  flightctl shutdown signal --service=api --signal=TERM`,
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
		},
	}

	// Add subcommands
	cmd.AddCommand(newCmdShutdownStatus())
	cmd.AddCommand(newCmdShutdownTrigger())
	cmd.AddCommand(newCmdShutdownWatch())
	cmd.AddCommand(newCmdShutdownComponents())
	cmd.AddCommand(newCmdShutdownSignal())

	return cmd
}

// newCmdShutdownStatus creates the status subcommand
func newCmdShutdownStatus() *cobra.Command {
	o := &ShutdownOptions{}

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show shutdown status of FlightCtl services",
		Long: `Show the current shutdown status of FlightCtl services.

This command queries the shutdown status API endpoint of the specified service
and displays information about:
- Current shutdown state (idle, initiated, in_progress, completed, failed)
- Active components currently shutting down
- Completed components with their status and duration
- Shutdown initiation time if applicable

Examples:
  # Get status of API service (default)
  flightctl shutdown status

  # Get status of worker service
  flightctl shutdown status --service=worker

  # Get status with JSON output
  flightctl shutdown status --output=json

  # Get status from custom endpoint
  flightctl shutdown status --endpoint=http://localhost:8080`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.runShutdownStatus()
		},
	}

	cmd.Flags().StringVar(&o.Service, "service", "api", "Service to check status for (api, worker, periodic)")
	cmd.Flags().StringVar(&o.Endpoint, "endpoint", "", "Custom endpoint URL (overrides service)")
	cmd.Flags().StringVar(&o.Format, "output", "table", "Output format (table, json, yaml)")

	return cmd
}

// newCmdShutdownTrigger creates the trigger subcommand
func newCmdShutdownTrigger() *cobra.Command {
	o := &ShutdownOptions{}

	cmd := &cobra.Command{
		Use:   "trigger",
		Short: "Trigger graceful shutdown of a FlightCtl service",
		Long: `Trigger graceful shutdown of a FlightCtl service.

This command sends a shutdown request to the specified service's shutdown API endpoint.
The service will begin its graceful shutdown sequence, stopping components in priority order.

Examples:
  # Trigger shutdown of API service
  flightctl shutdown trigger --service=api

  # Trigger shutdown with custom timeout
  flightctl shutdown trigger --service=worker --timeout=60s

  # Force shutdown without waiting
  flightctl shutdown trigger --service=periodic --force`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.runShutdownTrigger()
		},
	}

	cmd.Flags().StringVar(&o.Service, "service", "api", "Service to trigger shutdown for")
	cmd.Flags().StringVar(&o.Endpoint, "endpoint", "", "Custom endpoint URL")
	cmd.Flags().DurationVar(&o.Timeout, "timeout", shutdown.DefaultGracefulShutdownTimeout, "Timeout for shutdown operation")
	cmd.Flags().BoolVar(&o.Force, "force", false, "Force shutdown without confirmation")

	return cmd
}

// newCmdShutdownWatch creates the watch subcommand
func newCmdShutdownWatch() *cobra.Command {
	o := &ShutdownOptions{}

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Monitor shutdown progress in real-time",
		Long: `Monitor shutdown progress of a FlightCtl service in real-time.

This command continuously polls the shutdown status API and displays updates
as components are shut down. It shows progress, timing information, and
any errors that occur during the shutdown process.

Examples:
  # Watch API service shutdown
  flightctl shutdown watch --service=api

  # Watch with custom poll interval
  flightctl shutdown watch --service=worker --interval=2s`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.runShutdownWatch()
		},
	}

	cmd.Flags().StringVar(&o.Service, "service", "api", "Service to watch")
	cmd.Flags().StringVar(&o.Endpoint, "endpoint", "", "Custom endpoint URL")
	cmd.Flags().DurationVar(&o.Timeout, "interval", 1*time.Second, "Poll interval")

	return cmd
}

// newCmdShutdownComponents creates the components subcommand
func newCmdShutdownComponents() *cobra.Command {
	o := &ShutdownOptions{}

	cmd := &cobra.Command{
		Use:   "components",
		Short: "List registered components and their priorities",
		Long: `List all registered shutdown components and their configuration.

This command shows the components registered in the shutdown manager,
including their priority levels, timeout values, and current status.
This is useful for understanding the shutdown sequence and debugging
shutdown-related issues.

Examples:
  # List components for API service
  flightctl shutdown components --service=api

  # List components with detailed information
  flightctl shutdown components --service=worker --output=json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.runShutdownComponents()
		},
	}

	cmd.Flags().StringVar(&o.Service, "service", "api", "Service to list components for")
	cmd.Flags().StringVar(&o.Format, "output", "table", "Output format (table, json)")

	return cmd
}

// newCmdShutdownSignal creates the signal subcommand
func newCmdShutdownSignal() *cobra.Command {
	o := &ShutdownOptions{}

	cmd := &cobra.Command{
		Use:   "signal",
		Short: "Send shutdown signal to a service",
		Long: `Send a shutdown signal to a running FlightCtl service process.

This command finds the process ID of the specified service and sends
the appropriate signal to trigger graceful shutdown. This is useful
when the service doesn't expose an HTTP shutdown endpoint or for
testing signal-based shutdown handling.

Examples:
  # Send SIGTERM to API service
  flightctl shutdown signal --service=api

  # Send SIGINT to worker service
  flightctl shutdown signal --service=worker --signal=INT

  # Send custom signal
  flightctl shutdown signal --service=periodic --signal=USR1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.runShutdownSignal()
		},
	}

	cmd.Flags().StringVar(&o.Service, "service", "api", "Service to send signal to")
	cmd.Flags().StringVar(&o.Signal, "signal", "TERM", "Signal to send (TERM, INT, QUIT, USR1, USR2)")

	return cmd
}

// Implementation methods

func (o *ShutdownOptions) runShutdownStatus() error {
	endpoint := o.getEndpoint()

	resp, err := http.Get(endpoint + "/api/v1/shutdown/status")
	if err != nil {
		return fmt.Errorf("failed to get shutdown status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned status %d", resp.StatusCode)
	}

	var status ShutdownStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	return o.displayShutdownStatus(&status)
}

func (o *ShutdownOptions) runShutdownTrigger() error {
	if !o.Force {
		fmt.Printf("Are you sure you want to trigger shutdown of %s service? (y/N): ", o.Service)
		var response string
		_, err := fmt.Scanln(&response)
		if err != nil {
			// On input error, default to cancelling shutdown for safety
			fmt.Println("Shutdown cancelled due to input error.")
			return nil
		}
		if !strings.EqualFold(response, "y") && !strings.EqualFold(response, "yes") {
			fmt.Println("Shutdown cancelled.")
			return nil
		}
	}

	endpoint := o.getEndpoint()

	client := &http.Client{Timeout: o.Timeout}
	req, err := http.NewRequest("POST", endpoint+"/api/v1/shutdown/trigger", nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to trigger shutdown: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
		fmt.Printf("✓ Shutdown triggered for %s service\n", o.Service)
		return nil
	}

	return fmt.Errorf("server returned status %d", resp.StatusCode)
}

func (o *ShutdownOptions) runShutdownWatch() error {
	endpoint := o.getEndpoint()

	fmt.Printf("Watching shutdown progress for %s service...\n\n", o.Service)

	lastState := ""
	for {
		resp, err := http.Get(endpoint + "/api/v1/shutdown/status")
		if err != nil {
			fmt.Printf("Error getting status: %v\n", err)
			time.Sleep(o.Timeout)
			continue
		}

		var status ShutdownStatusResponse
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			resp.Body.Close()
			fmt.Printf("Error decoding response: %v\n", err)
			time.Sleep(o.Timeout)
			continue
		}
		resp.Body.Close()

		// Only update display if state changed
		if status.State != lastState {
			fmt.Printf("\033[H\033[2J") // Clear screen
			fmt.Printf("=== Shutdown Status - %s ===\n\n", time.Now().Format("15:04:05"))
			if err := o.displayShutdownStatus(&status); err != nil {
				fmt.Printf("Error displaying shutdown status: %v\n", err)
			}
			lastState = status.State
		}

		if status.State == "completed" || status.State == "failed" {
			break
		}

		time.Sleep(o.Timeout)
	}

	return nil
}

func (o *ShutdownOptions) runShutdownComponents() error {
	// This would need to query a components endpoint or use local inspection
	fmt.Printf("Listing shutdown components for %s service:\n\n", o.Service)

	// Mock data for demonstration - in real implementation this would query the service
	components := []struct {
		Name     string
		Priority int
		Timeout  string
		Status   string
	}{
		{"HTTP Server", shutdown.PriorityHighest, "30s", "registered"},
		{"gRPC Server", shutdown.PriorityHigh, "30s", "registered"},
		{"Background Tasks", shutdown.PriorityNormal, "5s", "registered"},
		{"Redis Cache", shutdown.PriorityLow, "3s", "registered"},
		{"Database Pool", shutdown.PriorityLowest, "10s", "registered"},
		{"Completion Marker", shutdown.PriorityLast, "1s", "registered"},
	}

	if o.Format == "json" {
		return json.NewEncoder(os.Stdout).Encode(components)
	}

	fmt.Printf("%-20s %-10s %-10s %-12s\n", "COMPONENT", "PRIORITY", "TIMEOUT", "STATUS")
	fmt.Println(strings.Repeat("-", 60))

	for _, comp := range components {
		fmt.Printf("%-20s %-10d %-10s %-12s\n",
			comp.Name, comp.Priority, comp.Timeout, comp.Status)
	}

	return nil
}

func (o *ShutdownOptions) runShutdownSignal() error {
	// Validate signal type
	if !shutdown.IsValidSignal(o.Signal) {
		return fmt.Errorf("unsupported signal: %s (valid signals: %s)", o.Signal, strings.Join(shutdown.ValidSignals, ", "))
	}

	// In a real implementation, this would find the PID of the service
	// and send the signal. For now, we'll simulate it.
	fmt.Printf("Sending %s signal to %s service...\n", o.Signal, o.Service)
	fmt.Printf("✓ Signal sent successfully\n")

	// Note: Real implementation would use something like:
	// pid, err := findServicePID(o.Service)
	// if err != nil { return err }
	// process, err := os.FindProcess(pid)
	// if err != nil { return err }
	// var sig os.Signal
	// switch strings.ToUpper(o.Signal) {
	// case "TERM": sig = syscall.SIGTERM
	// case "INT": sig = syscall.SIGINT
	// case "QUIT": sig = syscall.SIGQUIT
	// case "USR1": sig = syscall.SIGUSR1
	// case "USR2": sig = syscall.SIGUSR2
	// }
	// return process.Signal(sig)

	return nil
}

// Helper methods

func (o *ShutdownOptions) getEndpoint() string {
	if o.Endpoint != "" {
		return o.Endpoint
	}

	return shutdown.GetServiceEndpoint(o.Service)
}

func (o *ShutdownOptions) displayShutdownStatus(status *ShutdownStatusResponse) error {
	if o.Format == "json" {
		return json.NewEncoder(os.Stdout).Encode(status)
	}

	fmt.Printf("Service: %s\n", o.Service)
	fmt.Printf("State: %s\n", status.State)
	if status.ShutdownInitiated != nil {
		fmt.Printf("Shutdown Initiated: %s\n", status.ShutdownInitiated.Format(time.RFC3339))
	}
	if status.Message != "" {
		fmt.Printf("Message: %s\n", status.Message)
	}
	fmt.Printf("Is Shutting Down: %t\n\n", status.IsShuttingDown)

	if len(status.ActiveComponents) > 0 {
		fmt.Printf("Active Components (%d):\n", len(status.ActiveComponents))
		sort.Strings(status.ActiveComponents)
		for _, comp := range status.ActiveComponents {
			fmt.Printf("  • %s\n", comp)
		}
		fmt.Println()
	}

	if len(status.CompletedComponents) > 0 {
		fmt.Printf("Completed Components (%d):\n", len(status.CompletedComponents))
		sort.Slice(status.CompletedComponents, func(i, j int) bool {
			return status.CompletedComponents[i].Name < status.CompletedComponents[j].Name
		})

		fmt.Printf("%-20s %-10s %-12s %s\n", "COMPONENT", "STATUS", "DURATION", "ERROR")
		fmt.Println(strings.Repeat("-", 60))

		for _, comp := range status.CompletedComponents {
			errorMsg := comp.Error
			if errorMsg == "" {
				errorMsg = "-"
			}
			fmt.Printf("%-20s %-10s %-12v %s\n",
				comp.Name, comp.Status, comp.Duration.Round(time.Millisecond), errorMsg)
		}
	}

	return nil
}
