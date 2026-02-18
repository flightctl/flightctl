package main

import (
	"flag"
	"log"
	"os"
	"text/template"
)

type TemplateData struct {
	Flavor string
}

func main() {
	var (
		flavor       = flag.String("flavor", "el9", "Container flavor (el9 or el10)")
		templateFile = flag.String("template", "", "Path to template file")
		outputFile   = flag.String("output", "", "Path to output file")
	)
	flag.Parse()

	if *templateFile == "" || *outputFile == "" {
		log.Fatal("Both -template and -output flags are required")
	}

	// Validate flavor
	validFlavors := []string{"el9", "el10", "rhel9", "rhel10"}
	isValid := false
	for _, valid := range validFlavors {
		if *flavor == valid {
			isValid = true
			break
		}
	}
	if !isValid {
		log.Fatalf("Invalid flavor '%s'. Must be one of: el9, el10, rhel9, rhel10", *flavor)
	}

	// Read template
	tmplBytes, err := os.ReadFile(*templateFile)
	if err != nil {
		log.Fatalf("Failed to read template file %s: %v", *templateFile, err)
	}

	// Parse template
	tmpl, err := template.New("config").Parse(string(tmplBytes))
	if err != nil {
		log.Fatalf("Failed to parse template: %v", err)
	}

	// Create output file
	outFile, err := os.Create(*outputFile)
	if err != nil {
		log.Fatalf("Failed to create output file %s: %v", *outputFile, err)
	}
	defer outFile.Close()

	// Execute template
	data := TemplateData{
		Flavor: *flavor,
	}
	if err := tmpl.Execute(outFile, data); err != nil {
		log.Fatalf("Failed to execute template: %v", err)
	}

	log.Printf("Successfully rendered %s with flavor=%s to %s", *templateFile, *flavor, *outputFile)
}
