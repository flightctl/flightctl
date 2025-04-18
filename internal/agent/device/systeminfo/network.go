package systeminfo

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	// Default route timeout and interval
	defaultRouteTimeout  = 20 * time.Second
	defaultRouteInterval = 500 * time.Millisecond
)

// collectNetworkInfo gathers network interface information
func collectNetworkInfo(ctx context.Context, log *log.PrefixLogger, exec executer.Executer, reader fileio.Reader) (*NetworkInfo, error) {
	netInfo := &NetworkInfo{
		Interfaces: []InterfaceInfo{},
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("error getting network interfaces: %v", err)
	}

	for _, iface := range ifaces {
		ifaceInfo := InterfaceInfo{
			Name:        iface.Name,
			MACAddress:  iface.HardwareAddr.String(),
			IPAddresses: []string{},
			MTU:         iface.MTU,
		}

		ifaceInfo.IsVirtual = isVirtualInterface(iface.Name, reader)

		// ip addresses
		addrs, err := iface.Addrs()
		if err == nil {
			for _, addr := range addrs {
				ifaceInfo.IPAddresses = append(ifaceInfo.IPAddresses, addr.String())
			}
		}

		// interface status
		ifaceInfo.Status = getInterfaceStatus(iface.Name, reader)

		netInfo.Interfaces = append(netInfo.Interfaces, ifaceInfo)
	}

	// default route
	//
	// there's a potential race here if the IP is assigned via DHCP.
	// we wait briefly to allow the interface to come up and obtain an IP.
	// extending this wait could delay enrollment, so it's a trade-off.
	defaultRoute, err := waitForDefaultRoute(ctx, log, reader, defaultRouteTimeout, defaultRouteInterval)
	if err != nil {
		log.Warningf("Default route not available: %v", err)
	} else {
		log.Infof("Detected default route: %s via %s", defaultRoute.Gateway, defaultRoute.Interface)
		netInfo.DefaultRoute = defaultRoute
	}

	// dns servers
	dnsServers, err := getDNSServers(reader)
	if err == nil {
		netInfo.DNSServers = dnsServers
	}

	// fqdn
	fqdn, err := getFQDN(ctx, exec)
	if err == nil {
		netInfo.FQDN = fqdn
	}

	return netInfo, nil
}

// isVirtualInterface checks if the interface is a virtual interface
func isVirtualInterface(name string, reader fileio.Reader) bool {
	// check if the interface is in a virtual device directory
	sysDir := filepath.Join(sysVirtualNetDir, name)
	_, err := os.Stat(reader.PathFor(sysDir))
	if err == nil {
		return true
	}

	virtualPrefixes := []string{"veth", "virbr", "docker", "vnet", "tun", "tap", "br", "vlan", "bond"}
	for _, prefix := range virtualPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	return false
}

// getInterfaceStatus gets the operational state of the interface
func getInterfaceStatus(name string, reader fileio.Reader) string {
	stateFile := filepath.Join(sysClassNetDir, name, "operstate")
	content, err := reader.ReadFile(stateFile)
	if err != nil {
		return "unknown"
	}

	state := strings.TrimSpace(string(content))
	return state
}

// getDNSServers gets the DNS servers from /etc/resolv.conf
func getDNSServers(reader fileio.Reader) ([]string, error) {
	data, err := reader.ReadFile(resolveConfPath)
	if err != nil {
		return nil, err
	}

	var servers []string
	lines := strings.Split(string(data), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nameserver") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				servers = append(servers, fields[1])
			}
		}
	}

	return servers, nil
}

// getFQDN attempts to get the fully qualified domain name
func getFQDN(ctx context.Context, exec executer.Executer) (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", err
	}

	args := []string{"-f"}
	out, err := exec.CommandContext(ctx, "hostname", args...).Output()
	if err == nil {
		fqdn := strings.TrimSpace(string(out))
		if fqdn != "" && fqdn != hostname {
			return fqdn, nil
		}
	}

	// fail fast if we have lookup issues
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// fallback to DNS resolution via custom resolver to ensure ctx is honored
	r := &net.Resolver{}
	addrs, err := r.LookupHost(ctx, hostname)
	if err != nil || len(addrs) == 0 {
		return hostname, nil
	}

	names, err := r.LookupAddr(ctx, addrs[0])
	if err != nil || len(names) == 0 {
		return hostname, nil
	}

	return strings.TrimSuffix(names[0], "."), nil
}

// waitForDefaultRoute waits for the default route to be available
// within the specified timeout and interval
func waitForDefaultRoute(ctx context.Context, log *log.PrefixLogger, reader fileio.Reader, timeout, interval time.Duration) (*DefaultRoute, error) {
	var route *DefaultRoute

	err := wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		r, err := getDefaultRoute(reader)
		if err != nil {
			log.Debugf("retrying route detection: %v", err)
			return false, nil
		}
		route = r
		return true, nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to detect default route within %s: %w", timeout, err)
	}

	return route, nil
}

// getDefaultRoute attempts to determine the default gateway (IPv4 or IPv6)
func getDefaultRoute(reader fileio.Reader) (*DefaultRoute, error) {
	// IPv4 first
	route, err := getDefaultRouteIPv4(reader)
	if err == nil && !isLoopback(route.Interface) {
		return route, nil
	}

	// fallback to IPv6
	route, err = getDefaultRouteIPv6(reader)
	if err == nil && !isLoopback(route.Interface) {
		return route, nil
	}

	return nil, fmt.Errorf("no default route found (IPv4 or IPv6)")
}

// getDefaultRouteIPv4 attempts to determine the default IPv4 gateway
func getDefaultRouteIPv4(reader fileio.Reader) (*DefaultRoute, error) {
	data, err := reader.ReadFile(ipv4RoutePath)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 {
		return nil, fmt.Errorf("invalid route file format")
	}

	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		// is the default route (destination 0.0.0.0)
		if fields[1] == "00000000" {
			// gateway hex to IPv4
			gateway, err := hexToIPv4(fields[2])
			if err != nil {
				continue
			}

			return &DefaultRoute{
				Interface: fields[0],
				Gateway:   gateway,
				Family:    "ipv4",
			}, nil
		}
	}

	return nil, fmt.Errorf("default IPv4 route not found")
}

// getDefaultRouteIPv6 attempts to determine the default IPv6 gateway
func getDefaultRouteIPv6(reader fileio.Reader) (*DefaultRoute, error) {
	data, err := reader.ReadFile(ipv6RoutePath)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}

		// check if this is the default route (destination ::/0, which is 00000000000000000000000000000000 with prefix 00)
		// https://mirrors.deepspace6.net/Linux+IPv6-HOWTO/proc-net.html
		if fields[0] == "00000000000000000000000000000000" && fields[1] == "00" {
			// gateway hex to IPv6
			gateway, err := hexToIPv6(fields[4])
			if err != nil {
				continue
			}

			route := &DefaultRoute{
				Interface: fields[9],
				Gateway:   gateway,
				Family:    "ipv6",
			}

			return route, nil
		}
	}

	return nil, fmt.Errorf("default IPv6 route not found")
}

// hexToIPv4 converts an 8-character hex string to an IPv4 address
func hexToIPv4(hex string) (string, error) {
	if len(hex) != 8 {
		return "", fmt.Errorf("invalid IPv4 hex length")
	}

	// bytes are in reverse order from IPv4 notation:
	// - first 2 chars (hex[0:2]) represent the 4th octet
	// - second 2 chars (hex[2:4]) represent the 3rd octet
	// - third 2 chars (hex[4:6]) represent the 2nd octet
	// - fourth 2 chars (hex[6:8]) represent the 1st octet
	a, err := strconv.ParseUint(hex[6:8], 16, 8)
	if err != nil {
		return "", err
	}
	b, err := strconv.ParseUint(hex[4:6], 16, 8)
	if err != nil {
		return "", err
	}
	c, err := strconv.ParseUint(hex[2:4], 16, 8)
	if err != nil {
		return "", err
	}
	d, err := strconv.ParseUint(hex[0:2], 16, 8)
	if err != nil {
		return "", err
	}

	// return in proper order
	return fmt.Sprintf("%d.%d.%d.%d", a, b, c, d), nil
}

// hexToIPv6 converts a 32-character hex string to an IPv6 address
func hexToIPv6(hex string) (string, error) {
	if len(hex) != 32 {
		return "", fmt.Errorf("invalid IPv6 hex length")
	}

	var segments [8]string
	for i := 0; i < 8; i++ {
		segments[i] = hex[i*4 : (i+1)*4]
	}

	// join with colons to form standard IPv6 notation
	ipv6 := strings.Join(segments[:], ":")

	// get canonical format with zero compression
	parsedIP := net.ParseIP(ipv6)
	if parsedIP == nil {
		return "", fmt.Errorf("invalid IPv6 address format")
	}

	return parsedIP.String(), nil
}

// isLoopback checks if the given network interface name is a loopback interface
func isLoopback(name string) bool {
	return name == "lo" || strings.HasPrefix(name, "lo")
}
