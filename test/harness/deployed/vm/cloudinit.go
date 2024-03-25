package vm

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func (b *BootcVMCommon) ParseCloudInit() (err error) {
	if b.hasCloudInit {
		if b.cloudInitDir == "" {
			return errors.New("empty cloud init directory")
		}

		err = b.createCiDataIso(b.cloudInitDir)
		if err != nil {
			return fmt.Errorf("creating cloud-init iso: %w", err)
		}

		ciDataIso := filepath.Join(b.testDir, b.vmName+"-cloudinit-data.iso")
		b.cloudInitArgs = ciDataIso
	}

	return nil
}

func (b *BootcVMCommon) createCiDataIso(inDir string) error {
	isoOutFile := filepath.Join(b.testDir, b.vmName+"-cloudinit-data.iso")

	args := []string{"-output", isoOutFile}
	args = append(args, "-volid", "cidata", "-joliet", "-rock", "-partition_cyl_align", "on")
	args = append(args, inDir)

	cmd := exec.Command("xorrisofs", args...)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}
