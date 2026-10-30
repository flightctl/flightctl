package vm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// VerifyArtifact checks the guest's checksum against a confined, regular output file.
func (s *Session) VerifyArtifact(ctx context.Context, relativePath string) (string, error) {
	file, err := s.openArtifact(relativePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var guest bytes.Buffer
	if err := s.Run(ctx, []string{"sha256sum", "--", filepath.Join("/output", relativePath)}, nil, nil, &guest); err != nil {
		return "", err
	}
	fields := strings.Fields(guest.String())
	if len(fields) == 0 {
		return "", fmt.Errorf("guest did not provide an artifact checksum")
	}
	hash := sha256.New()
	buf := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := file.Read(buf)
		if n > 0 {
			_, _ = hash.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != fields[0] {
		return "", fmt.Errorf("artifact checksum mismatch")
	}
	return filepath.Join(s.outputDir, relativePath), nil
}

func (s *Session) openArtifact(relativePath string) (*os.File, error) {
	if !filepath.IsLocal(relativePath) {
		return nil, fmt.Errorf("artifact path must be relative to the output directory")
	}
	root, err := os.OpenRoot(s.outputDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	components := strings.Split(filepath.Clean(relativePath), string(os.PathSeparator))
	for i := range components {
		info, err := root.Lstat(filepath.Join(components[:i+1]...))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("artifact path contains a symlink")
		}
	}
	file, err := root.OpenFile(relativePath, artifactOpenFlags(), 0)
	if err != nil {
		return nil, err
	}
	valid := false
	defer func() {
		if !valid {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, fmt.Errorf("artifact must be a nonempty regular file")
	}
	valid = true
	return file, nil
}

// ReadArtifact reads bounded guest metadata without following links or blocking on special files.
func (s *Session) ReadArtifact(ctx context.Context, relativePath string, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if maxBytes < 1 {
		return nil, fmt.Errorf("artifact size limit must be positive")
	}
	file, err := s.openArtifact(relativePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("artifact exceeds size limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("artifact exceeds size limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}
