package auxiliary

import (
	"crypto/x509"
	"net"
	"testing"
)

const (
	registryCertificateHostname = "registry.example.test"
	registryCertificateIPv4     = "192.168.129.73"
)

func TestRegistryCertificateSANs(t *testing.T) {
	tests := []struct {
		host       string
		wantDNS    string
		wantIP     string
		wantDNSLen int
		wantIPLen  int
	}{
		{
			host:       registryCertificateHostname,
			wantDNS:    registryCertificateHostname,
			wantDNSLen: 3,
			wantIPLen:  1,
		},
		{
			host:       registryCertificateIPv4,
			wantIP:     registryCertificateIPv4,
			wantDNSLen: 2,
			wantIPLen:  2,
		},
	}

	for _, test := range tests {
		dnsNames, ipAddresses := registryCertificateSANs(test.host)
		if len(dnsNames) != test.wantDNSLen {
			t.Errorf("registryCertificateSANs(%q) DNS SAN count = %d, want %d", test.host, len(dnsNames), test.wantDNSLen)
		}
		if test.wantDNS != "" && !containsString(dnsNames, test.wantDNS) {
			t.Errorf("registryCertificateSANs(%q) DNS SANs = %v, want %q", test.host, dnsNames, test.wantDNS)
		}
		if len(ipAddresses) != test.wantIPLen {
			t.Errorf("registryCertificateSANs(%q) IP SAN count = %d, want %d", test.host, len(ipAddresses), test.wantIPLen)
		}
		if test.wantIP != "" && !containsIP(ipAddresses, net.ParseIP(test.wantIP)) {
			t.Errorf("registryCertificateSANs(%q) IP SANs = %v, want %q", test.host, ipAddresses, test.wantIP)
		}
	}
}

func TestRegistryCertificateMatchesHost(t *testing.T) {
	tests := []struct {
		name    string
		cert    *x509.Certificate
		host    string
		want    bool
		wantErr bool
	}{
		{name: "When the current IPv4 host is in the certificate it should reuse the registry", cert: &x509.Certificate{IPAddresses: []net.IP{net.ParseIP(registryCertificateIPv4)}}, host: registryCertificateIPv4, want: true},
		{name: "When the current IPv4 host is absent it should recreate the registry", cert: &x509.Certificate{IPAddresses: []net.IP{net.ParseIP("192.168.122.140")}}, host: registryCertificateIPv4},
		{name: "When the current DNS host is in the certificate it should reuse the registry", cert: &x509.Certificate{DNSNames: []string{registryCertificateHostname}}, host: registryCertificateHostname, want: true},
		{name: "When the certificate is missing it should return an error", host: registryCertificateIPv4, wantErr: true},
	}

	for _, test := range tests {
		got, err := registryCertificateMatchesHost(test.cert, test.host)
		if test.wantErr {
			if err == nil {
				t.Errorf("%s: registryCertificateMatchesHost() error = nil, want error", test.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: registryCertificateMatchesHost() error = %v", test.name, err)
			continue
		}
		if got != test.want {
			t.Errorf("%s: registryCertificateMatchesHost() = %t, want %t", test.name, got, test.want)
		}
	}
}

func containsIP(addresses []net.IP, want net.IP) bool {
	for _, address := range addresses {
		if address.Equal(want) {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
