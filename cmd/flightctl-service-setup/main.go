package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/pkg/setup"
)

var (
	inputFile    = flag.String("input", "/app/service-config.yaml", "Path to the service configuration file")
	templatesDir = flag.String("templates", "/app/templates", "Directory containing template files")
	outputDir    = flag.String("output", "/app/rendered", "Directory for rendered output files")
)

func main() {
	flag.Parse()

	err := setup.RenderTemplates(*inputFile, *templatesDir, *outputDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error rendering templates: %v\n", err)
		os.Exit(1)
	}
}
