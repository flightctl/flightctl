package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

func NewPrepareWorkerEnrollmentCommand() *cobra.Command {
	var pkiDir, runtimeDir, configFile string
	cmd := &cobra.Command{
		Use:   "prepare-worker-enrollment",
		Short: "Prepare a private volatile snapshot of available worker enrollment signing material",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.NewWorkerFromFile(configFile)
			if err != nil {
				_, cleanupErr := prepareWorkerEnrollment(pkiDir, runtimeDir, false)
				return errors.Join(fmt.Errorf("cannot load validated worker configuration: %w", err), cleanupErr)
			}
			enabled := cfg.CA != nil && cfg.CA.InternalConfig != nil && cfg.CA.CAType == ca.InternalCA && cfg.CA.DeviceEnrollmentSignerName != "" && cfg.CA.InternalConfig.SerialFile == ""
			available, err := prepareWorkerEnrollment(pkiDir, runtimeDir, enabled)
			if err != nil {
				return err
			}
			if !available {
				cmd.PrintErrln("enrollment signing unavailable; prepared empty worker snapshot")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&pkiDir, "pki-dir", "/etc/flightctl/pki", "Existing PKI input directory")
	cmd.Flags().StringVar(&configFile, "config", "/etc/flightctl/flightctl-worker/config.yaml", "Rendered worker configuration")
	cmd.Flags().StringVar(&runtimeDir, "runtime-dir", "/run/flightctl-worker-enrollment", "Private runtime directory prepared by the service manager")
	return cmd
}

func openProjectionDirectory(path string) (*os.Root, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("projection paths must be clean absolute paths")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, fmt.Errorf("cannot open safe projection directory")
		}
		_ = unix.Close(fd)
		fd = next
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return nil, err
		}
		if (stat.Uid != 0 && uint64(stat.Uid) != uint64(os.Geteuid())) || (stat.Mode&0022 != 0 && stat.Mode&unix.S_ISVTX == 0) {
			return nil, fmt.Errorf("projection directory ownership or permissions are unsafe")
		}
	}
	return os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", fd))
}

type projectionFile struct {
	data []byte
	mode os.FileMode
}

func readProjectionFile(root *os.Root, name string) (projectionFile, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0400 == 0 {
		return projectionFile{}, fmt.Errorf("signing input is unavailable")
	}
	file, err := root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return projectionFile{}, err
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return projectionFile{}, err
	}
	if (stat.Uid != 0 && uint64(stat.Uid) != uint64(os.Geteuid())) || stat.Mode&0022 != 0 {
		return projectionFile{}, fmt.Errorf("unsafe signing input permissions")
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return projectionFile{}, fmt.Errorf("signing input changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 {
		return projectionFile{}, fmt.Errorf("cannot read signing input")
	}
	return projectionFile{data: data, mode: info.Mode().Perm() & 0600}, nil
}

func enrollmentProjectionInputs(pkiDir string) (map[string]projectionFile, bool) {
	issuer, err := openProjectionDirectory(filepath.Join(pkiDir, "flightctl-api"))
	if err != nil {
		return nil, false
	}
	defer issuer.Close()
	pki, err := openProjectionDirectory(pkiDir)
	if err != nil {
		return nil, false
	}
	defer pki.Close()
	files := make(map[string]projectionFile, 3)
	for _, name := range []string{"client-signer.crt", "client-signer.key", "ca-bundle.crt"} {
		source := issuer
		if name == "ca-bundle.crt" {
			source = pki
		}
		file, err := readProjectionFile(source, name)
		if err != nil {
			return nil, false
		}
		files[name] = file
	}
	pair, err := tls.X509KeyPair(files["client-signer.crt"].data, files["client-signer.key"].data)
	if err != nil {
		return nil, false
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || crypto.ValidateSigningCA(certificate) != nil {
		return nil, false
	}
	bundle := bytes.TrimSpace(files["ca-bundle.crt"].data)
	if len(bundle) == 0 {
		return nil, false
	}
	for len(bundle) > 0 {
		if !bytes.HasPrefix(bundle, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, false
		}
		block, rest := pem.Decode(bundle)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, false
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return nil, false
		}
		bundle = bytes.TrimSpace(rest)
	}
	return files, true
}

func prepareWorkerEnrollment(pkiDir, runtimeDir string, enabled bool) (bool, error) {
	runtime, err := openProjectionDirectory(runtimeDir)
	if err != nil {
		return false, err
	}
	defer runtime.Close()
	info, err := runtime.Stat(".")
	if err != nil || info.Mode().Perm() != 0700 {
		return false, fmt.Errorf("worker runtime directory must have mode 0700")
	}
	for _, name := range []string{"next", "signing"} {
		info, err := runtime.Lstat(name)
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return false, fmt.Errorf("unsafe worker snapshot destination")
		}
		if err != nil && !os.IsNotExist(err) {
			return false, err
		}
	}
	if err := runtime.RemoveAll("next"); err != nil {
		return false, err
	}
	if err := runtime.Mkdir("next", 0700); err != nil {
		return false, err
	}
	defer func() { _ = runtime.RemoveAll("next") }()
	var files map[string]projectionFile
	var available bool
	if enabled {
		files, available = enrollmentProjectionInputs(pkiDir)
	}
	for name, file := range files {
		if err := runtime.WriteFile("next/"+name, file.data, file.mode); err != nil {
			return false, err
		}
	}
	if err := runtime.RemoveAll("signing"); err != nil {
		return false, err
	}
	if err := runtime.Rename("next", "signing"); err != nil {
		return false, err
	}
	return available, nil
}
