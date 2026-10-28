//go:build !linux

package vm

import (
	"os"
	"os/exec"
)

func configureProcess(_ *exec.Cmd) {}

func artifactOpenFlags() int { return os.O_RDONLY }
