package deviceidprocessor

import (
	"context"

	"github.com/flightctl/flightctl/internal/otel-collector/cnauthenticator"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

type deviceIdProcessor struct{}

func (p *deviceIdProcessor) processMetrics(ctx context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
	// Extract device_id from context using the correct ContextKey type
	deviceIDVal := ctx.Value(cnauthenticator.DeviceIDKey)
	if deviceIDStr, ok := deviceIDVal.(string); ok && deviceIDStr != "" {
		rms := md.ResourceMetrics()
		for i := 0; i < rms.Len(); i++ {
			resource := rms.At(i).Resource()
			attrs := resource.Attributes()
			attrs.PutStr("device_id", deviceIDStr)
		}
	}

	// Extract org_id from context using the correct ContextKey type
	orgIDVal := ctx.Value(cnauthenticator.OrgIDKey)
	if orgIDStr, ok := orgIDVal.(string); ok && orgIDStr != "" {
		rms := md.ResourceMetrics()
		for i := 0; i < rms.Len(); i++ {
			resource := rms.At(i).Resource()
			attrs := resource.Attributes()
			attrs.PutStr("org_id", orgIDStr)
		}
	}

	return md, nil
}
