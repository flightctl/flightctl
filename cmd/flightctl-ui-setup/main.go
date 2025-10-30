package main

import (
	"fmt"
	"os"

	"github.com/flightctl/flightctl/pkg/setup"
)

const inputFile = "/app/service-config.yaml"
const templatesDir = "/app/templates"
const outputDir = "/app/rendered"

func main() {
	err := setup.RenderTemplates(inputFile, templatesDir, outputDir)
	if err != nil {
		fmt.Printf("Error rendering templates: %v\n", err)
		os.Exit(1)
	}

	os.Exit(0)
}
