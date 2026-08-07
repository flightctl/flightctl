package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/sirupsen/logrus"
)

func resolveRuntimeSourceIPs(log *logrus.Logger, explicit []string, iface, base string, count, prefixLen int) ([]net.IP, error) {
	explicitSet := len(explicit) > 0
	rangeSet := iface != "" || base != "" || count > 0
	if explicitSet && rangeSet {
		return nil, fmt.Errorf("--source-ips is mutually exclusive with --source-ip-iface/--source-ip-base/--source-ip-count")
	}
	if explicitSet {
		return parseExplicitSourceIPs(log, explicit)
	}
	if !rangeSet {
		return nil, nil
	}
	// Non-root path: expand the same range flags used by setup; do not create addresses.
	_ = prefixLen
	ips, err := expandSourceIPRange(base, count)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		log.Infof("Using source IP: %s", ip.String())
	}
	if iface != "" {
		log.Infof("source IP range expanded for interface %s (addresses must already exist)", iface)
	}
	return ips, nil
}

func parseExplicitSourceIPs(log *logrus.Logger, explicit []string) ([]net.IP, error) {
	ips := make([]net.IP, 0, len(explicit))
	for _, ipStr := range explicit {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			return nil, fmt.Errorf("invalid source IP address: %s", ipStr)
		}
		ips = append(ips, ip)
		log.Infof("Using source IP: %s", ip.String())
	}
	return ips, nil
}

func setupSourceIPs(log *logrus.Logger, iface, base string, count, prefixLen int) ([]net.IP, error) {
	if err := validateSourceIPSetupArgs(iface, base, count, prefixLen); err != nil {
		return nil, err
	}
	ips, err := expandSourceIPRange(base, count)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		cidr := fmt.Sprintf("%s/%d", ip.String(), prefixLen)
		added, err := ensureAddrOnIface(iface, cidr)
		if err != nil {
			return nil, fmt.Errorf("adding %s on %s: %w (run this setup step as root)", cidr, iface, err)
		}
		if added {
			log.Infof("added source IP %s on %s", cidr, iface)
		} else {
			log.Infof("source IP %s already present on %s", cidr, iface)
		}
	}
	return ips, nil
}

func teardownSourceIPs(log *logrus.Logger, iface, base string, count, prefixLen int) error {
	if err := validateSourceIPSetupArgs(iface, base, count, prefixLen); err != nil {
		return err
	}
	ips, err := expandSourceIPRange(base, count)
	if err != nil {
		return err
	}
	var firstErr error
	for _, ip := range ips {
		cidr := fmt.Sprintf("%s/%d", ip.String(), prefixLen)
		cmd := exec.Command("ip", "addr", "del", cidr, "dev", iface)
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := string(out)
			if strings.Contains(msg, "Cannot find address") || strings.Contains(msg, "Address not found") {
				log.Infof("source IP %s already absent from %s", cidr, iface)
				continue
			}
			log.Warnf("removing source IP %s from %s: %v (%s)", cidr, iface, err, strings.TrimSpace(msg))
			if firstErr == nil {
				firstErr = fmt.Errorf("removing %s from %s: %w (run this teardown step as root)", cidr, iface, err)
			}
			continue
		}
		log.Infof("removed source IP %s from %s", cidr, iface)
	}
	return firstErr
}

func validateSourceIPSetupArgs(iface, base string, count, prefixLen int) error {
	if iface == "" {
		return fmt.Errorf("--source-ip-iface is required")
	}
	if prefixLen < 1 || prefixLen > 32 {
		return fmt.Errorf("--source-ip-prefix must be between 1 and 32")
	}
	_, err := expandSourceIPRange(base, count)
	return err
}

func expandSourceIPRange(base string, count int) ([]net.IP, error) {
	if base == "" || count <= 0 {
		return nil, fmt.Errorf("--source-ip-base and --source-ip-count (>0) are required")
	}
	baseIP := net.ParseIP(base)
	if baseIP == nil || baseIP.To4() == nil {
		return nil, fmt.Errorf("--source-ip-base must be a valid IPv4 address: %s", base)
	}
	ips := make([]net.IP, 0, count)
	for i := 0; i < count; i++ {
		ips = append(ips, nextIPv4(baseIP.To4(), i))
	}
	return ips, nil
}

func sourceIPsFlagValue(ips []net.IP) string {
	parts := make([]string, len(ips))
	for i, ip := range ips {
		parts[i] = ip.String()
	}
	return strings.Join(parts, ",")
}

func ensureAddrOnIface(iface, cidr string) (added bool, err error) {
	cmd := exec.Command("ip", "addr", "add", cidr, "dev", iface)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	msg := string(out)
	if strings.Contains(msg, "File exists") || strings.Contains(msg, "already assigned") {
		return false, nil
	}
	return false, fmt.Errorf("%w: %s", err, strings.TrimSpace(msg))
}

func nextIPv4(ip net.IP, n int) net.IP {
	v := binary.BigEndian.Uint32(ip.To4())
	v += uint32(n)
	out := make(net.IP, 4)
	binary.BigEndian.PutUint32(out, v)
	return out
}
