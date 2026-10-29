package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
)

// runRegistryReferrers runs in the image storage owner's process context. This
// keeps containers registry policy and credential-helper execution aligned with
// the Skopeo commands used to inspect and pull the image.
func runRegistryReferrers(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("registry-referrers", flag.ContinueOnError)
	image := flags.String("image", "", "Repository and manifest digest to discover referrers for")
	authFile := flags.String("authfile", "", "Containers registry authentication file")
	noCredentials := flags.Bool("no-creds", false, "Disable default registry credential lookup")
	timeout := flags.Duration("timeout", 2*time.Minute, "Timeout for registry discovery")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if *image == "" || flags.NArg() != 0 || *timeout <= 0 {
		return fmt.Errorf("registry-referrers requires --image and a positive --timeout")
	}
	if *noCredentials && *authFile != "" {
		return fmt.Errorf("--no-creds and --authfile are mutually exclusive")
	}
	opts := []client.ClientOption{client.WithPullSecret(*authFile)}
	if !*noCredentials {
		opts = append(opts, client.WithDefaultAuth())
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	index, err := client.ListRegistryReferrers(ctx, rw, *image, opts...)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(index)
}
