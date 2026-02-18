package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flightctl/flightctl/hack/pkg/flavors"
	"gopkg.in/yaml.v3"
)

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: flavorctl [--overlay <path>]... <command> [args...]

Overlays can also be specified via the FLAVOR_OVERLAYS environment variable
(colon-separated paths). CLI --overlay arguments are applied after env overlays.

Commands:
  list                        List available flavors
  get <flavor> <dot.path>     Get a single value (e.g. "el9 helm.timeouts.apiReadiness")
  export-build <flavor>       Print shell export lines for the build section
  dump <flavor> <section>     Dump a section as YAML (e.g. "el9 helm")
  merged-images <flavor> <helm|quadlets>  Merged images for a target tool`)
	os.Exit(1)
}

func main() {
	var overlayPaths []string
	if ov := os.Getenv("FLAVOR_OVERLAYS"); ov != "" {
		overlayPaths = append(overlayPaths, strings.Split(ov, ":")...)
	}
	args := os.Args[1:]
	for len(args) >= 2 && args[0] == "--overlay" {
		overlayPaths = append(overlayPaths, args[1])
		args = args[2:]
	}

	if len(args) < 1 {
		usage()
	}

	basePath := findBasePath()
	data, err := flavors.LoadMerged(basePath, overlayPaths)
	fatal(err)

	cmd := args[0]
	args = args[1:]

	switch cmd {
	case "list":
		names := flavors.ListFlavors(data)
		sort.Strings(names)
		for _, n := range names {
			fmt.Println(n)
		}

	case "get":
		if len(args) != 2 {
			usage()
		}
		flavor, dotPath := args[0], args[1]
		flavorData, ok := data[flavor]
		if !ok {
			fatal(fmt.Errorf("flavor %q not found", flavor))
		}
		fm, ok := flavorData.(map[string]any)
		if !ok {
			fatal(fmt.Errorf("flavor %q is not a map", flavor))
		}
		val, err := flavors.Navigate(fm, dotPath)
		fatal(err)
		fmt.Println(stringify(val))

	case "export-build":
		if len(args) != 1 {
			usage()
		}
		fatal(flavors.PrintExportBuild(data, args[0]))

	case "dump":
		if len(args) != 2 {
			usage()
		}
		flavor, section := args[0], args[1]
		flavorData, ok := data[flavor]
		if !ok {
			fatal(fmt.Errorf("flavor %q not found", flavor))
		}
		fm, ok := flavorData.(map[string]any)
		if !ok {
			fatal(fmt.Errorf("flavor %q is not a map", flavor))
		}
		val, err := flavors.Navigate(fm, section)
		fatal(err)
		out, err := yaml.Marshal(val)
		fatal(err)
		fmt.Print(string(out))

	case "merged-images":
		if len(args) != 2 {
			usage()
		}
		flavor, target := args[0], args[1]
		flavorData, ok := data[flavor]
		if !ok {
			fatal(fmt.Errorf("flavor %q not found", flavor))
		}
		fm, ok := flavorData.(map[string]any)
		if !ok {
			fatal(fmt.Errorf("flavor %q is not a map", flavor))
		}
		merged, err := flavors.MergeImages(fm, flavor, target)
		fatal(err)
		out, err := yaml.Marshal(merged)
		fatal(err)
		fmt.Print(string(out))

	default:
		usage()
	}
}

func findBasePath() string {
	if env := os.Getenv("FLAVORS_YAML"); env != "" {
		return env
	}
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "..", "..", "flavors.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	wd, err := os.Getwd()
	fatal(err)
	for dir := wd; ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "hack", "flavors.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	fmt.Fprintln(os.Stderr, "error: cannot find hack/flavors.yaml; set FLAVORS_YAML or run from the repo root")
	os.Exit(1)
	return ""
}

func stringify(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case int:
		return fmt.Sprintf("%d", val)
	case float64:
		if val == float64(int(val)) {
			return fmt.Sprintf("%d", int(val))
		}
		return fmt.Sprintf("%g", val)
	case bool:
		return fmt.Sprintf("%t", val)
	case map[string]any:
		out, _ := yaml.Marshal(val)
		return strings.TrimSpace(string(out))
	default:
		return fmt.Sprint(val)
	}
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
