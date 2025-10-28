package main

import (
	"fmt"
	"os"

	renderservices "github.com/flightctl/flightctl/internal/render_services"
)

func main() {
	err := renderservices.RenderServicesConfig()
	if err != nil {
		fmt.Printf("Error rendering services config: %v\n", err)
		os.Exit(1)
	}

	os.Exit(0)
}
