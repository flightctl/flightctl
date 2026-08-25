//go:build linux

package onboarding_test

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// WiFi soft-AP onboarding tests (EDM-4205).
//
// The onboarding image ships a config-driven WiFi access point (hostapd +
// dnsmasq + a captive portal) so a headless device with no wired network can be
// onboarded over the air: a technician associates a phone/laptop to the AP, any
// captive-portal probe is redirected to the Cockpit onboarding wizard, and the
// wizard configures + enrolls the device.
//
// These specs exercise that stack end to end against a real KVM guest. The AP
// needs a WiFi radio, which the guest does not physically have, so we synthesize
// two virtual radios with mac80211_hwsim: one drives the AP (hostapd), the other
// acts as an over-the-air client (associate, DHCP, reach the portal). All WiFi
// activity is confined to those hwsim radios; the guest's single SLIRP NIC
// (10.0.2.15) remains the untouched SSH/Cockpit control channel.
//
// The specs are SSH/curl-driven verification against the deployed onboarding
// scripts (setup-wifi-ap.sh, cleanup-onboarding.sh) and units
// (flightctl-onboarding-wifi-ap@, -dnsmasq@, -captive-portal@). Unlike the
// config-flow suite, they do NOT drive the wizard through Chrome: the full
// wizard flow is already covered there, and the novel surface here is the radio
// layer (SSID broadcast, association, DHCP, DNS hijack, captive-portal
// redirects). W5 asserts the exact redirect *target* is the Cockpit wizard URL,
// and W2 proves a real client reaches the portal over the air — together that is
// the substance of "wizard via redirect" without the flakiness of rendering
// Cockpit over an emulated radio link.
//
// The suite's BeforeSuite installs the WiFi soft-AP stack (hostapd, dnsmasq,
// wpa_supplicant, NetworkManager-wifi, iw) and the mac80211_hwsim kernel module
// transiently via `dnf --transient` (see installWifiStack), so these specs run
// on the standard e2e device image with no dedicated baked image. The
// wifiStackAvailable check below is a defensive guard: if that install ever
// fails, the specs skip rather than fail with an opaque hostapd/modprobe error.

const (
	wifiAPAddress    = "10.42.0.1"
	wifiAPSubnet     = "10.42.0." // lease prefix handed out by dnsmasq
	wifiPassword     = "onboarding"
	wifiConfigPath   = "/etc/cockpit/system-onboarding/config.json"
	wifiSetupScript  = "/usr/libexec/flightctl-onboarding/setup-wifi-ap.sh"
	wifiCleanup      = "/usr/libexec/flightctl-onboarding/cleanup-onboarding.sh"
	wifiMarkerDir    = "/var/lib/flightctl-onboarding"
	wifiRuntimeDir   = "/run/flightctl-onboarding"
	wifiFirewallZone = "fc-onboarding-ap"
	hostapdBinary    = "/usr/sbin/hostapd"

	// wifiWizardURL is where captive-portal.py redirects generic connectivity
	// probes: http://<ap-address>:<cockpit-port>/<cockpit onboarding path>.
	wifiWizardURL = "http://10.42.0.1:9090/cockpit/@localhost/system-onboarding/index.html"

	// wifiProbeHostname is an arbitrary external connectivity-probe hostname used
	// to prove the AP's DNS is a catch-all: dnsmasq must answer ANY name with the
	// AP address so every OS captive-portal probe is funneled to the portal.
	wifiProbeHostname = "connectivitycheck.gstatic.com"

	// wifiSSHProbeTimeout bounds the quick SSH probes (wifiStackAvailable's
	// command/modprobe checks and resolveViaDNS) so a wedged SSH connection cannot
	// stall a spec's BeforeEach skip decision or an Eventually poller.
	wifiSSHProbeTimeout = 30 * time.Second
)

// A NIC name (nmcli DEVICE) and the SSID we expect, constrained so nothing
// harvested from the VM is interpolated into a shell command unchecked.
var (
	wifiIfaceRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	wifiSSIDRE  = regexp.MustCompile(`^flightctl-[0-9A-Za-z]+$`)
)

// runShell runs a single shell command line on the VM (pipes/redirection allowed)
// and returns its stdout. RunSSH space-joins a multi-element argv, so anything
// with shell metacharacters must be passed as one element; callers here only
// interpolate values validated against the regexps above.
func runShell(h *e2e.Harness, script string) (string, error) {
	out, err := h.VM.RunSSH([]string{script}, nil)
	if out == nil {
		return "", err
	}
	return out.String(), err
}

// wifiStackAvailable reports whether the WiFi soft-AP stack is present (hostapd
// binary + a loadable mac80211_hwsim module). The suite's BeforeSuite installs it
// transiently (installWifiStack); this is a defensive guard so the specs skip
// rather than fail opaquely if that install did not take effect.
//
// The module probe is `modprobe -n` (dry run), not `modinfo`: modinfo only
// confirms the .ko file exists in the modules tree, whereas modprobe -n resolves
// the full dependency chain and honors modules.dep exactly as the real
// `modprobe mac80211_hwsim radios=2` in loadHwsimRadios will. That makes the gate
// a true predictor of load success (e.g. it fails when depmod has not registered
// the freshly-dropped module), so the specs skip cleanly instead of loadHwsimRadios
// failing hard on an unregistered module.
func wifiStackAvailable(h *e2e.Harness) bool {
	// Bound both probes: wifiStackAvailable runs in BeforeEach for every spec, so a
	// wedged SSH connection must not hang the skip decision indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), wifiSSHProbeTimeout)
	defer cancel()
	if _, err := h.VM.RunSSHContext(ctx, []string{"command", "-v", "hostapd"}, nil); err != nil {
		return false
	}
	if _, err := h.VM.RunSSHContext(ctx, []string{"sudo", "modprobe", "-n", "mac80211_hwsim"}, nil); err != nil {
		return false
	}
	return true
}

// wifiDevices returns the WiFi interface names NetworkManager sees, sorted.
func wifiDevices(h *e2e.Harness) []string {
	out, err := h.VM.RunSSH([]string{"nmcli", "-t", "-f", "DEVICE,TYPE", "device"}, nil)
	if err != nil || out == nil {
		return nil
	}
	var devs []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		fields := splitNmcliTerse(line)
		if len(fields) >= 2 && fields[1] == "wifi" && wifiIfaceRE.MatchString(fields[0]) {
			devs = append(devs, fields[0])
		}
	}
	sort.Strings(devs)
	return devs
}

// loadHwsimRadios loads two virtual radios and returns their interface names.
// The first drives the AP, the second acts as the over-the-air client.
func loadHwsimRadios(h *e2e.Harness) (apIface, clientIface string) {
	_, err := h.VM.RunSSH([]string{"sudo", "modprobe", "mac80211_hwsim", "radios=2"}, nil)
	Expect(err).ToNot(HaveOccurred(), "failed to load mac80211_hwsim")

	var devs []string
	Eventually(func() int {
		devs = wifiDevices(h)
		return len(devs)
	}, 30*time.Second, 2*time.Second).Should(BeNumerically(">=", 2),
		"expected at least two mac80211_hwsim WiFi interfaces")
	return devs[0], devs[1]
}

// configureWifiAP enables the WiFi AP in the onboarding config, pinning the AP
// interface (empty to leave auto-detection to the script). extra is an optional
// trailing jq expression (e.g. " | .runOnce = false"). jq edits the file in
// place via a temp file so the onboardingUser password already in the config is
// never echoed back over SSH.
func configureWifiAP(h *e2e.Harness, iface, extra string) {
	Expect(iface == "" || wifiIfaceRE.MatchString(iface)).To(BeTrue(), "invalid WiFi interface %q", iface)
	ifaceField := ""
	if iface != "" {
		ifaceField = fmt.Sprintf(`,"interface":%q`, iface)
	}
	prog := fmt.Sprintf(`.network.wifiAp = {"enabled":true,"password":%q,"address":%q%s}%s`,
		wifiPassword, wifiAPAddress, ifaceField, extra)
	cmd := fmt.Sprintf(`sudo jq '%s' %s > /tmp/wifi-config.json && sudo mv /tmp/wifi-config.json %s`,
		prog, wifiConfigPath, wifiConfigPath)
	_, err := h.VM.RunSSH([]string{cmd}, nil)
	Expect(err).ToNot(HaveOccurred(), "failed to write wifiAp config")
}

// startWifiAP runs setup-wifi-ap.sh and waits for the AP to be fully up: the
// wifi-ap@ service active (hostapd running) and the captive portal serving on
// the AP address (proves the AP address was assigned and the portal is bound).
func startWifiAP(h *e2e.Harness, iface string) {
	_, err := h.VM.RunSSH([]string{"sudo", "bash", wifiSetupScript}, nil)
	Expect(err).ToNot(HaveOccurred(), "setup-wifi-ap.sh failed")

	unit := fmt.Sprintf("flightctl-onboarding-wifi-ap@%s.service", iface)
	Eventually(func() string {
		return systemctlIsActive(h, unit)
	}, 60*time.Second, 3*time.Second).Should(Equal("active"),
		"WiFi AP service did not become active")

	Eventually(func() string {
		code, _ := curlStatus(h, "http://"+wifiAPAddress+"/device-info")
		return code
	}, 60*time.Second, 3*time.Second).Should(Equal("200"),
		"captive portal did not come up on the AP address")
}

// apSSID reads and validates the device-specific SSID from the generated hostapd
// config. The suffix is derived from the DMI serial or permanent MAC, so we only
// assert its shape (flightctl-<suffix>) and reuse the exact value for scans and
// association.
func apSSID(h *e2e.Harness, iface string) string {
	out, err := runShell(h, fmt.Sprintf("sudo sed -n 's/^ssid=//p' /run/flightctl-onboarding/hostapd-%s.conf", iface))
	Expect(err).ToNot(HaveOccurred(), "failed to read generated hostapd config")
	ssid := strings.TrimSpace(out)
	Expect(wifiSSIDRE.MatchString(ssid)).To(BeTrue(), "unexpected SSID %q", ssid)
	return ssid
}

// systemctlIsActive returns the trimmed `systemctl is-active` state for a unit
// (e.g. "active", "inactive", "failed"). is-active exits non-zero when not
// active; we read the printed state and ignore the exit code.
func systemctlIsActive(h *e2e.Harness, unit string) string {
	out, _ := runShell(h, "systemctl is-active "+unit)
	return strings.TrimSpace(out)
}

// curlStatus returns the HTTP status code for a request. curlArgs is appended to
// curl verbatim, so callers must only pass validated interface names / constant
// URLs.
func curlStatus(h *e2e.Harness, curlArgs string) (string, error) {
	out, err := runShell(h, fmt.Sprintf("curl -s -o /dev/null -w '%%{http_code}' %s", curlArgs))
	return strings.TrimSpace(out), err
}

// curlRedirect returns the HTTP status code and the redirect target (Location).
func curlRedirect(h *e2e.Harness, curlArgs string) (code, location string) {
	out, _ := runShell(h, fmt.Sprintf("curl -s -o /dev/null -w '%%{http_code} %%{redirect_url}' %s", curlArgs))
	parts := strings.Fields(strings.TrimSpace(out))
	if len(parts) >= 1 {
		code = parts[0]
	}
	if len(parts) >= 2 {
		location = parts[1]
	}
	return code, location
}

// dnsQueryScript resolves a single A record against an explicit DNS server using
// only the Python 3 standard library, so it needs no bind-utils (dig/nslookup)
// on the read-only device image. python3 is guaranteed present here: the captive
// portal (captive-portal.py) is a python3 service and startWifiAP already proved
// it is serving. The hostname and server are passed as argv (argv[1], argv[2])
// so nothing is interpolated into the script text. It prints the resolved IPv4
// address and exits 0, or exits non-zero if the query fails or returns no A record.
const dnsQueryScript = `
import socket, struct, sys
host, server = sys.argv[1], sys.argv[2]
header = struct.pack(">HHHHHH", 0x1234, 0x0100, 1, 0, 0, 0)
q = b"".join(struct.pack("B", len(p)) + p.encode() for p in host.split(".")) + b"\x00"
q += struct.pack(">HH", 1, 1)
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.settimeout(5)
s.sendto(header + q, (server, 53))
data, _ = s.recvfrom(512)
ancount = struct.unpack(">H", data[6:8])[0]
i = 12
while data[i] != 0:
    i += 1 + data[i]
i += 5
for _ in range(ancount):
    if data[i] & 0xC0 == 0xC0:
        i += 2
    else:
        while data[i] != 0:
            i += 1 + data[i]
        i += 1
    rtype, _rclass, _ttl, rdlen = struct.unpack(">HHIH", data[i:i+10])
    i += 10
    rdata = data[i:i+rdlen]
    i += rdlen
    if rtype == 1 and rdlen == 4:
        print("%d.%d.%d.%d" % tuple(rdata))
        sys.exit(0)
sys.exit(1)
`

// resolveViaDNS asks the given DNS server to resolve hostname and returns the
// A record it answers with. hostname/server are validated constants passed as
// argv to dnsQueryScript (never spliced into the script text).
func resolveViaDNS(h *e2e.Harness, hostname, server string) (string, error) {
	// Bound the probe: resolveViaDNS runs inside an Eventually, so a wedged SSH
	// connection must not outlive the poller's own deadline.
	ctx, cancel := context.WithTimeout(context.Background(), wifiSSHProbeTimeout)
	defer cancel()
	out, err := h.VM.RunSSHContext(ctx, []string{"python3", "-", hostname, server}, bytes.NewBufferString(dnsQueryScript))
	if out == nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), err
}

// expectSSIDVisible waits until the client radio can see the AP's SSID in a scan.
func expectSSIDVisible(h *e2e.Harness, clientIface, ssid string) {
	Eventually(func() (string, error) {
		_, _ = h.VM.RunSSH([]string{"sudo", "nmcli", "device", "wifi", "rescan", "ifname", clientIface}, nil)
		return runShell(h, fmt.Sprintf("nmcli -t -f SSID device wifi list ifname %s", clientIface))
	}, 90*time.Second, 5*time.Second).Should(ContainSubstring(ssid),
		"client radio should see the broadcast SSID %q", ssid)
}

var _ = Describe("Onboarding WiFi access point", Label("onboarding"), Label("wifi"), func() {
	var h *e2e.Harness

	BeforeEach(func() {
		h = e2e.GetWorkerHarness()
		if !wifiStackAvailable(h) {
			Skip("WiFi stack (hostapd + mac80211_hwsim) not available; " +
				"the suite installs it transiently in BeforeSuite (see installWifiStack)")
		}
	})

	It("When the AP is enabled it should broadcast the configured SSID", Label("90434"), func() {
		apIface, clientIface := loadHwsimRadios(h)
		configureWifiAP(h, apIface, "")
		startWifiAP(h, apIface)

		// The generated hostapd config carries a device-specific SSID...
		ssid := apSSID(h, apIface)
		// ...and it is actually on the air: the client radio can scan for it.
		expectSSIDVisible(h, clientIface, ssid)
	})

	It("When a client associates it should get a lease pointing at the AP and reach the portal", Label("90436"), func() {
		apIface, clientIface := loadHwsimRadios(h)
		configureWifiAP(h, apIface, "")
		startWifiAP(h, apIface)

		ssid := apSSID(h, apIface)
		expectSSIDVisible(h, clientIface, ssid)

		// Associate the client radio with the AP over the air.
		_, err := h.VM.RunSSH([]string{
			"sudo", "nmcli", "device", "wifi", "connect", ssid,
			"password", wifiPassword, "ifname", clientIface,
		}, nil)
		Expect(err).ToNot(HaveOccurred(), "client failed to associate with the AP")

		// dnsmasq DHCP: lease in the AP subnet, gateway (option 3) and DNS
		// (option 6) both the AP — the DNS option is what hijacks the client's
		// name resolution to the captive portal.
		Eventually(func() (string, error) {
			return runShell(h, fmt.Sprintf("nmcli -t -f IP4.ADDRESS,IP4.GATEWAY,IP4.DNS device show %s", clientIface))
		}, 60*time.Second, 3*time.Second).Should(And(
			MatchRegexp(`IP4\.ADDRESS.*:`+regexp.QuoteMeta(wifiAPSubnet)+`\d+`),
			MatchRegexp(`IP4\.GATEWAY:`+regexp.QuoteMeta(wifiAPAddress)),
			MatchRegexp(`IP4\.DNS.*:`+regexp.QuoteMeta(wifiAPAddress)),
		), "client did not get the expected DHCP configuration from the AP")

		// DNS hijack: the client's resolver (the AP, per the IP4.DNS assertion
		// above) must answer an arbitrary external hostname with the AP address.
		// Asserting this closes the gap the direct-IP probe leaves open - a broken
		// catch-all DNS could still pass the DHCP and IP-based redirect checks.
		var resolved string
		Eventually(func() (string, error) {
			var err error
			resolved, err = resolveViaDNS(h, wifiProbeHostname, wifiAPAddress)
			return resolved, err
		}, 30*time.Second, 3*time.Second).Should(Equal(wifiAPAddress),
			"AP DNS should resolve arbitrary hostname %q to the AP address (captive-portal hijack)", wifiProbeHostname)

		// Reaching that hostname over the air (via the address DNS just returned)
		// is redirected by the portal - the full name-based captive-portal path,
		// not just a direct-IP hit.
		Eventually(func() string {
			code, _ := curlRedirect(h, fmt.Sprintf("--interface %s --resolve %s:80:%s http://%s/generate_204",
				clientIface, wifiProbeHostname, resolved, wifiProbeHostname))
			return code
		}, 30*time.Second, 3*time.Second).Should(Equal("302"),
			"associated client should be redirected by the captive portal")
	})

	It("When OS connectivity probes hit the portal they should be redirected", Label("90438"), func() {
		apIface, _ := loadHwsimRadios(h)
		configureWifiAP(h, apIface, "")
		startWifiAP(h, apIface)

		// Android (/generate_204) and Windows (/ncsi.txt, /connecttest.txt)
		// probes are redirected to the wizard.
		for _, path := range []string{"/generate_204", "/ncsi.txt", "/connecttest.txt"} {
			code, loc := curlRedirect(h, "http://"+wifiAPAddress+path)
			Expect(code).To(Equal("302"), "probe %s should redirect", path)
			Expect(loc).To(ContainSubstring("/system-onboarding/"),
				"probe %s should redirect to the wizard, got %q", path, loc)
		}

		// The Apple CNA (/hotspot-detect.html) is a limited WebKit view that
		// cannot run Cockpit, so it is redirected to the device-info page instead.
		code, loc := curlRedirect(h, "http://"+wifiAPAddress+"/hotspot-detect.html")
		Expect(code).To(Equal("302"))
		Expect(loc).To(ContainSubstring("/device-info"),
			"Apple CNA probe should redirect to device-info, got %q", loc)
	})

	It("When the device-info page is fetched it should show the serial and WiFi MAC", Label("90440"), func() {
		apIface, _ := loadHwsimRadios(h)
		configureWifiAP(h, apIface, "")
		startWifiAP(h, apIface)

		macOut, err := runShell(h, fmt.Sprintf("cat /sys/class/net/%s/address", apIface))
		Expect(err).ToNot(HaveOccurred())
		mac := strings.ToUpper(strings.TrimSpace(macOut))

		body, err := runShell(h, "curl -s http://"+wifiAPAddress+"/device-info")
		Expect(err).ToNot(HaveOccurred())
		Expect(body).To(ContainSubstring("Serial Number"))
		Expect(body).To(ContainSubstring("WiFi MAC Address"))
		Expect(body).To(ContainSubstring(mac), "device-info should show the AP interface MAC")
	})

	It("When redirected by the portal it should target the onboarding wizard URL", Label("90442"), func() {
		apIface, _ := loadHwsimRadios(h)
		configureWifiAP(h, apIface, "")
		startWifiAP(h, apIface)

		_, loc := curlRedirect(h, "http://"+wifiAPAddress+"/generate_204")
		Expect(loc).To(Equal(wifiWizardURL),
			"captive portal should redirect to the Cockpit onboarding wizard")
	})

	It("When hostapd is unavailable it should skip AP setup gracefully", Label("90465"), func() {
		// No pinned interface and no hwsim needed: the hostapd check runs before
		// interface detection, so this exercises the graceful-degradation path.
		configureWifiAP(h, "", "")

		// Hide hostapd from the setup script without mutating the read-only image:
		// bind-mounting /dev/null over the binary makes `command -v hostapd` fail
		// (a char device is not executable). The mount is dropped again after.
		_, err := h.VM.RunSSH([]string{"sudo", "mount", "--bind", "/dev/null", hostapdBinary}, nil)
		Expect(err).ToNot(HaveOccurred(), "failed to shadow hostapd")
		defer func() {
			_, _ = h.VM.RunSSH([]string{"sudo", "umount", hostapdBinary}, nil)
		}()

		out, err := h.VM.RunSSH([]string{"sudo", "bash", wifiSetupScript}, nil)
		Expect(err).ToNot(HaveOccurred(), "setup-wifi-ap.sh should exit 0 when hostapd is missing")
		Expect(out.String()).To(ContainSubstring("hostapd is not installed"),
			"setup should warn that hostapd is unavailable")

		// No AP service instance should have been started.
		units, _ := runShell(h, "systemctl list-units --plain --no-legend 'flightctl-onboarding-wifi-ap@*.service' 2>/dev/null || true")
		Expect(strings.TrimSpace(units)).To(BeEmpty(),
			"no WiFi AP unit should be started when hostapd is missing")
	})

	It("When onboarding completes it should stop the AP and remove its runtime state", Label("90444"), func() {
		apIface, _ := loadHwsimRadios(h)
		// runOnce=false keeps the onboarding user/session during cleanup; the AP
		// teardown happens regardless of runOnce.
		configureWifiAP(h, apIface, " | .runOnce = false")
		startWifiAP(h, apIface)

		// cleanup-onboarding.sh only tears down once onboarding is marked complete.
		_, err := h.VM.RunSSH([]string{"sudo", "mkdir", "-p", wifiMarkerDir}, nil)
		Expect(err).ToNot(HaveOccurred())
		_, err = h.VM.RunSSH([]string{"sudo", "touch", wifiMarkerDir + "/.onboarding-complete"}, nil)
		Expect(err).ToNot(HaveOccurred())

		// Run cleanup; assert on its effects rather than its exit code (the script
		// also (re)starts the agent and other best-effort steps that can warn).
		out, cleanupErr := h.VM.RunSSH([]string{"sudo", "bash", wifiCleanup}, nil)
		if cleanupErr != nil {
			GinkgoWriter.Printf("cleanup-onboarding.sh exited non-zero: %v\n%s\n", cleanupErr, out.String())
		}

		Expect(systemctlIsActive(h, fmt.Sprintf("flightctl-onboarding-wifi-ap@%s.service", apIface))).ToNot(Equal("active"),
			"WiFi AP service should be stopped after cleanup")
		Expect(systemctlIsActive(h, fmt.Sprintf("flightctl-onboarding-dnsmasq@%s.service", apIface))).ToNot(Equal("active"),
			"dnsmasq service should be stopped after cleanup")
		Expect(systemctlIsActive(h, fmt.Sprintf("flightctl-onboarding-captive-portal@%s.service", apIface))).ToNot(Equal("active"),
			"captive portal service should be stopped after cleanup")

		// Runtime state (hostapd/dnsmasq configs, env file) is removed.
		_, err = h.VM.RunSSH([]string{"sudo", "test", "-d", wifiRuntimeDir}, nil)
		Expect(err).To(HaveOccurred(), "runtime dir %s should be removed by cleanup", wifiRuntimeDir)

		// The dedicated firewalld zone is removed (only when firewalld is running).
		if systemctlIsActive(h, "firewalld") == "active" {
			zones, _ := runShell(h, "sudo firewall-cmd --get-zones 2>/dev/null || true")
			Expect(zones).ToNot(ContainSubstring(wifiFirewallZone),
				"onboarding firewall zone should be removed after cleanup")
		}
	})
})
