package vm

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func (v *TestVM) ParseCloudInit() error {
	if v.hasCloudInit {
		if v.CloudInitDir == "" {
			return errors.New("empty cloud init directory")
		}

		err := v.createCiDataIso(v.CloudInitDir)
		if err != nil {
			return fmt.Errorf("creating cloud-init iso: %w", err)
		}

		ciDataIso := filepath.Join(v.TestDir, v.VMName+"-cloudinit-data.iso")
		v.cloudInitArgs = ciDataIso
	}

	return nil
}

func (v *TestVM) createCiDataIso(inDir string) error {
	isoOutFile := filepath.Join(v.TestDir, v.VMName+"-cloudinit-data.iso")

	args := []string{"-output", isoOutFile}
	args = append(args, "-volid", "cidata", "-joliet", "-rock", "-partition_cyl_align", "on")
	args = append(args, inDir)

	cmd := exec.Command("xorrisofs", args...)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}
