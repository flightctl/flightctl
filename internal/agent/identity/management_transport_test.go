package identity

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestManagementLongPollsShareHTTP2Connection(t *testing.T) {
	for _, tpmBacked := range []bool{false, true} {
		name := map[bool]string{false: "file", true: "TPM"}[tpmBacked]
		t.Run("When identity is "+name+" it should share mTLS HTTP2 across policy and rendered requests", func(t *testing.T) {
			ctrl := gomock.NewController(t)
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			require.NoError(t, err)
			template := &x509.Certificate{
				SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "device"},
				NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
				KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
			}
			der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
			require.NoError(t, err)
			certificate, err := x509.ParseCertificate(der)
			require.NoError(t, err)
			certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
			keyDER, err := x509.MarshalPKCS8PrivateKey(key)
			require.NoError(t, err)
			clientCAs := x509.NewCertPool()
			clientCAs.AddCert(certificate)
			type request struct {
				address string
				proto   int
				peer    string
			}
			requests := make(chan request, 2)
			release := make(chan struct{})
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- request{address: r.RemoteAddr, proto: r.ProtoMajor, peer: r.TLS.PeerCertificates[0].Subject.CommonName}
				if strings.HasSuffix(r.URL.Path, "/firmwarepolicy") {
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			server.EnableHTTP2 = true
			server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientCAs}
			server.StartTLS()
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			config := &baseclient.Config{Service: baseclient.Service{
				Server:                   server.URL,
				CertificateAuthorityData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}),
			}, AuthInfo: baseclient.AuthInfo{ClientCertificateData: certPEM, ClientKeyData: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})}}
			config.HTTPOptions = []baseclient.HTTPClientOption{func(c *http.Client) error {
				t.Cleanup(c.CloseIdleConnections)
				return nil
			}}
			var provider Provider
			if tpmBacked {
				tpmClient := tpm.NewMockClient(ctrl)
				tpmClient.EXPECT().GetSigner().Return(key)
				provider = &tpmProvider{client: tpmClient, certificateData: certPEM}
			} else {
				rw := fileio.NewMockReadWriter(ctrl)
				rw.EXPECT().PathExists(gomock.Any()).Return(true, nil)
				provider = &fileProvider{rw: rw, log: log.NewPrefixLogger("transport")}
			}
			management, err := provider.CreateManagementClient(config, nil)
			require.NoError(t, err)
			completed := make(chan error, 1)
			go func() {
				_, status, err := management.GetFirmwarePolicy(ctx, "device", firmwarepolicy.Receipt{})
				if err == nil && status != http.StatusNoContent {
					err = fmt.Errorf("policy status %d", status)
				}
				completed <- err
			}()
			var first request
			select {
			case first = <-requests:
			case err := <-completed:
				t.Fatalf("policy request ended before reaching server: %v", err)
			case <-ctx.Done():
				t.Fatal("policy request did not reach server")
			}
			_, status, err := management.GetRenderedDevice(ctx, "device", nil)
			require.NoError(t, err)
			require.Equal(t, http.StatusNoContent, status)
			second := <-requests
			require.Equal(t, 2, first.proto)
			require.Equal(t, 2, second.proto)
			require.Equal(t, "device", first.peer)
			require.Equal(t, "device", second.peer)
			require.Equal(t, first.address, second.address, "concurrent long polls must share the established connection")
			close(release)
			require.NoError(t, <-completed)
		})
	}
}
