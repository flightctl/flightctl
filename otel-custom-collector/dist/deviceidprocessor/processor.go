package deviceidprocessor

import (
	"context"

	"go.opentelemetry.io/collector/pdata/pmetric"
)

type processor2 struct{}

func (p *processor2) processMetrics(ctx context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
	val := ctx.Value("device_fingerprint")
	if valStr, ok := val.(string); ok && valStr != "" {
		rms := md.ResourceMetrics()
		for i := 0; i < rms.Len(); i++ {
			resource := rms.At(i).Resource()
			attrs := resource.Attributes()
			attrs.PutStr("device_id", valStr)
		}
	}
	return md, nil
}
