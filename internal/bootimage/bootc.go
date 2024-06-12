package bootimage

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	CmdBootc = "bootc"
)

var _ Manager = (*OsTreeClient)(nil)

type BootcClient struct {
	executer     executer.Executer
	osTreeClient *OsTreeClient
	log          *log.PrefixLogger
}

// NewBootcCmd creates a new bootc client.
func NewBootcClient(log *log.PrefixLogger, executer executer.Executer) (*BootcClient, error) {
	_, err := exec.LookPath(CmdBootc)
	if err != nil {
		return nil, fmt.Errorf("look path failed: %w", err)
	}

	c := &BootcClient{
		executer: executer,
		log:      log,
	}

	// add osTreeClient support if available
	osTreeClient, err := NewOsTreeClient("flightctl-agent", executer)
	if err != nil {
		log.Debugf("failed to get osTreeClient: %v", err)
	} else {
		c.osTreeClient = osTreeClient
	}

	return c, nil
}

// Status returns the current bootc host status.
func (b *BootcClient) Status(ctx context.Context) (*HostStatus, error) {
	args := []string{"status", "--json"}
	stdout, stderr, exitCode := b.executer.ExecuteWithContext(ctx, CmdBootc, args...)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("get bootc status: %s", stderr)
	}

	var host BootcHost
	if err := json.Unmarshal([]byte(stdout), &host); err != nil {
		return nil, fmt.Errorf("unmarshalling config file: %w", err)
	}

	return &host.Status, nil
}

// Switch pulls the specified image and stages it for the next boot while retaining a copy of the most recently booted image.
// The status will be updated in logger.
func (b *BootcClient) Switch(ctx context.Context, image string) error {
	done := make(chan error, 1)
	go func() {
		args := []string{"switch", "--retain", image}
		_, stderr, exitCode := b.executer.ExecuteWithContext(ctx, CmdBootc, args...)
		if exitCode != 0 {
			done <- fmt.Errorf("stage image: %s", stderr)
			return
		}
		done <- nil
	}()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	start := time.Now()
	// log progress
	for {
		select {
		case err := <-done:
			b.log.Infof("Switching image complete took: %v", time.Since(start))
			return err
		case <-ticker.C:
			b.log.Infof("Switching image, please wait...")
		case <-ctx.Done():
			b.log.Infof("Switching image failed after: %v", time.Since(start))
			return ctx.Err()
		}
	}
}

// Apply restart or reboot into the new target image.
func (b *BootcClient) Apply(ctx context.Context) error {
	args := []string{"upgrade", "--apply"}
	_, stderr, exitCode := b.executer.ExecuteWithContext(ctx, CmdBootc, args...)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if exitCode != 0 {
		return fmt.Errorf("apply image: %s", stderr)
	}
	return nil
}

func (b *BootcClient) RemoveRollback(ctx context.Context) error {
	if b.osTreeClient != nil {
		return b.osTreeClient.RemoveRollback(ctx)
	}
	b.log.Debugf("Remove rollback is not supported by bootc")
	return nil
}

func (b *BootcClient) RemoveStaged(ctx context.Context) error {
	if b.osTreeClient != nil {
		return b.osTreeClient.RemoveStaged(ctx)
	}
	b.log.Debugf("Remove staged is not supported by bootc")
	return nil
}

func (b *BootcClient) IsDisabled() bool {
	return false
}

// UsrOverlay adds a transient writable overlayfs on `/usr` that will be discarded on reboot.
func (b *BootcClient) UsrOverlay(ctx context.Context) error {
	args := []string{"usr-overlay"}
	_, stderr, exitCode := b.executer.ExecuteWithContext(ctx, CmdBootc, args...)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if exitCode != 0 {
		return fmt.Errorf("overlay image: %s", stderr)
	}
	return nil
}
