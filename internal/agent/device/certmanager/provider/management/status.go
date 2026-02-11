package management

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo"
	systeminfocommon "github.com/flightctl/flightctl/internal/agent/device/systeminfo/common"
	"github.com/flightctl/flightctl/internal/agent/identity"
	"github.com/flightctl/flightctl/pkg/certmanager"
	"k8s.io/client-go/util/cert"
)

func WithStatusCollectOnStore(
	ctx context.Context,
	log certmanager.Logger,
	identityProvider identity.Provider,
	systemInfoManager systeminfo.Manager,
	statusManager status.Manager,
	f certmanager.StorageFactory,
) certmanager.StorageFactory {
	if identityProvider == nil || systemInfoManager == nil || statusManager == nil || f == nil {
		return f
	}

	systemInfoManager.RegisterCollector(ctx, systeminfocommon.ManagementCertSerialKey, func(ctx context.Context) string {
		pemBytes, err := identityProvider.GetCertificate()
		if err != nil {
			return ""
		}

		certs, err := cert.ParseCertsPEM(pemBytes)
		if err != nil || len(certs) == 0 || certs[0] == nil || certs[0].SerialNumber == nil {
			return ""
		}

		b := certs[0].SerialNumber.Bytes()
		if len(b) == 0 {
			return ""
		}

		// Colon-separated hex (e.g. 46:E5:42:26:2B:79:34:F2)
		out := make([]byte, 0, len(b)*3-1)
		for i, v := range b {
			if i > 0 {
				out = append(out, ':')
			}
			out = append(out, fmt.Sprintf("%02X", v)...)
		}

		return string(out)
	})

	systemInfoManager.RegisterCollector(ctx, systeminfocommon.ManagementCertNotAfterKey, func(ctx context.Context) string {
		pemBytes, err := identityProvider.GetCertificate()
		if err != nil {
			return ""
		}

		certs, err := cert.ParseCertsPEM(pemBytes)
		if err != nil || len(certs) == 0 || certs[0] == nil {
			return ""
		}

		return certs[0].NotAfter.UTC().Format(time.RFC3339)
	})

	return &chainStorageFactory{
		next: f,
		store: func(ctx context.Context, next certmanager.StorageProvider, req certmanager.StoreRequest) error {
			if err := next.Store(ctx, req); err != nil {
				return err
			}

			// Side-effect: best-effort status refresh. Do NOT fail the cert store if it breaks.
			if err := statusManager.Collect(ctx, status.WithForceCollect()); err != nil {
				if log != nil {
					log.Errorf("Management cert renewal succeeded, but status collection after renewal failed: %v", err)
				}
			}

			return nil
		},
	}
}
