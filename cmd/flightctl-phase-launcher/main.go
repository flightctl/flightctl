//go:build linux

package main

import (
	"fmt"
	"os"

	"github.com/flightctl/flightctl/pkg/phasebootstrap"
)

func main() {
	if err := phasebootstrap.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "firmware phase launch proof failed")
		os.Exit(125)
	}
}
