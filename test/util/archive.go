package util

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ExtractTarballContents returns a list of filenames in the tarball
func ExtractTarballContents(tarballPath string) ([]string, error) {
	file, err := os.Open(tarballPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	var contents []string

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		if header.Typeflag == tar.TypeReg {
			contents = append(contents, header.Name)
		}
	}

	return contents, nil
}

// ExtractTarball extracts a tarball to the specified directory
func ExtractTarball(tarballPath, destDir string) error {
	file, err := os.Open(tarballPath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}

			outFile, err := os.Create(target)
			if err != nil {
				return err
			}

			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()
		}
	}

	return nil
}

// ValidateMustGatherArchive validates that a must-gather archive contains expected files
func ValidateMustGatherArchive(archivePath string, expectedFiles []string) error {
	contents, err := ExtractTarballContents(archivePath)
	if err != nil {
		return fmt.Errorf("failed to extract archive contents: %w", err)
	}

	for _, expectedFile := range expectedFiles {
		found := false
		for _, actualFile := range contents {
			if actualFile == expectedFile {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("expected file %s not found in archive", expectedFile)
		}
	}

	return nil
}
