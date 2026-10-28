package tasks

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/imagebuilder_worker/vm"
)

// validateGuestOutput runs after guest shutdown, when output paths can no longer change.
func validateGuestOutput(ctx context.Context, outputDir string) error {
	return filepath.WalkDir(outputDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("guest output contains a symlink")
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("guest output contains a special file")
		}
		return nil
	})
}

func verifyGuestDirectory(ctx context.Context, session *vm.Session, outputDir, dir string) error {
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(outputDir, path)
		if err != nil {
			return err
		}
		_, err = session.VerifyArtifact(ctx, rel)
		return err
	})
}
