package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent"
	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/version"
)

func main() {
	args := os.Args[1:]

	if len(args) == 0 || isAgentFlags(args) {
		if hasHelpFlag(args) {
			printAgentHelp()
			os.Exit(0)
		}

		flag.CommandLine = flag.NewFlagSet("agent", flag.ExitOnError)
		command := NewAgentCommand()
		if err := command.Execute(); err != nil {
			os.Exit(1)
		}
		return
	}

	switch args[0] {
	case "-h", "--help":
		printUsage()
		os.Exit(0)

	case "version":
		printVersion()
		os.Exit(0)

	case "system-info":
		flag.CommandLine = flag.NewFlagSet("system-info", flag.ExitOnError)
		command := NewSystemInfoCommand()
		if err := command.Execute(); err != nil {
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown sub‑command %q\n\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

type agentCmd struct {
	log        *log.PrefixLogger
	config     *config.Config
	configFile string
}

func NewAgentCommand() *agentCmd {
	a := &agentCmd{
		log: log.NewPrefixLogger(""),
	}

	flag.StringVar(&a.configFile, "config", config.DefaultConfigFile, "Path to the agent's configuration file.")
	flag.Parse()

	var err error
	a.config, err = config.Load(a.configFile)
	if err != nil {
		a.log.Fatalf("Error loading config: %v", err)
	}

	a.log.Level(a.config.LogLevel)
	a.log.Infof("Loaded configuration: %s", a.config.StringSanitized())

	return a
}

func (a *agentCmd) Execute() error {
	agentInstance := agent.New(a.log, a.config, a.configFile)
	if err := agentInstance.Run(context.Background()); err != nil {
		a.log.Fatalf("running device agent: %v", err)
	}
	return nil
}

type systemInfoCmd struct {
	log             *log.PrefixLogger
	hardwareMapPath string
}

func NewSystemInfoCommand() *systemInfoCmd {
	fs := flag.NewFlagSet("system-info", flag.ExitOnError)
	cmd := &systemInfoCmd{
		log: log.NewPrefixLogger(""),
	}

	fs.StringVar(&cmd.hardwareMapPath, "hardware-map", "", "Path to the hardware map file.")

	if hasHelpFlag(os.Args[2:]) {
		fmt.Println("Usage of system-info:")
		fs.PrintDefaults()
		os.Exit(0)
	}

	if err := fs.Parse(os.Args[2:]); err != nil {
		cmd.log.Fatalf("Error parsing flags: %v", err)
	}

	cmd.log.Level("info")
	return cmd
}

func (s *systemInfoCmd) Execute() error {
	reader := fileio.NewReader()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	exec := &executer.CommonExecuter{}
	info, err := systeminfo.Collect(ctx, s.log, exec, reader, nil, s.hardwareMapPath, systeminfo.WithAll())
	if err != nil {
		s.log.Fatalf("Error collecting system info: %v", err)
	}

	jsonBytes, err := json.Marshal(info)
	if err != nil {
		s.log.Fatalf("Error serializing system info to JSON: %v", err)
	}

	fmt.Println(string(jsonBytes))

	return nil
}

func printUsage() {
	fmt.Printf("Usage of %s:\n", os.Args[0])
	fmt.Println("commands:")
	fmt.Println("  version      Display version information")
	fmt.Println("  system-info  Display system information")
	fmt.Println("")
	fmt.Println("Run '<command> --help' for command-specific flags.")
	fmt.Println("flags:")
	flag.CommandLine.SetOutput(os.Stdout)
	flag.PrintDefaults()
}

func printAgentHelp() {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	var configFile string
	fs.StringVar(&configFile, "config", config.DefaultConfigFile, "Path to the agent's configuration file.")
	fmt.Printf("Usage of %s (agent mode):\n", os.Args[0])
	fs.PrintDefaults()
}

func printVersion() {
	versionInfo := version.Get()
	fmt.Printf("Agent Version: %s\n", versionInfo.String())
	fmt.Printf("Git Commit: %s\n", versionInfo.GitCommit)
}

func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

func isAgentFlags(args []string) bool {
	// there is no agent subcommand so we assume if the first arg is a flag it
	// is against the agent
	return len(args) > 0 && strings.HasPrefix(args[0], "-")
}
