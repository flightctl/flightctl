package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/internal/agent"
	"github.com/flightctl/flightctl/pkg/log"
)

func main() {
	command := NewAgentCommand()
	if err := command.Execute(); err != nil {
		os.Exit(1)
	}
}

type agentCmd struct {
	log        *log.PrefixLogger
	config     *agent.Config
	configFile string
}

func NewAgentCommand() *agentCmd {
	a := &agentCmd{
		log:    log.NewPrefixLogger(""),
		config: agent.NewDefault(),
	}

	flag.StringVar(&a.configFile, "config", agent.DefaultConfigFile, "Path to the agent's configuration file.")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", os.Args[0])
		fmt.Println("This program starts an agent with the specified configuration. Below are the available flags:")
		flag.PrintDefaults()
	}

	flag.Parse()

	if err := a.config.ParseConfigFile(a.configFile); err != nil {
		a.log.Fatalf("Error parsing config: %v", err)
	}
	if err := a.config.Complete(); err != nil {
		a.log.Fatalf("Error completing config: %v", err)
	}
	if err := a.config.Validate(); err != nil {
		a.log.Fatalf("Error validating config: %v", err)
	}

	a.log.Level(a.config.LogLevel)

	return a
}

func (a *agentCmd) Execute() error {
	agentInstance := agent.New(a.log, a.config)
	if err := agentInstance.Run(context.Background()); err != nil {
		a.log.Fatalf("running device agent: %v", err)
	}
	return nil
}
