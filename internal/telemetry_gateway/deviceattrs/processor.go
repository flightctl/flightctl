package deviceattrs

import (
	"context"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/internal/telemetry_gateway/deviceauth"
	"github.com/google/uuid"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

type deviceattrs struct{}

var (
	// Signals the processor refuses to process unauthenticated data.
	ErrUnauthenticated = errors.New("device unauthenticated")
)

func (p *deviceattrs) processMetrics(ctx context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
	var devID, orgID string

	if v := ctx.Value(deviceauth.DeviceIDKey); v != nil {
		if s, ok := v.(string); ok && s != "" {
			devID = s
		}
	}

	if devID == "" {
		return pmetric.NewMetrics(),
			consumererror.NewPermanent(fmt.Errorf("%w: missing device_id in context", ErrUnauthenticated))
	}

	if v := ctx.Value(deviceauth.DeviceOrgIDKey); v != nil {
		switch t := v.(type) {
		case uuid.UUID:
			orgID = t.String()
		case string:
			orgID = t
		}
	}

	if orgID == "" {
		return pmetric.NewMetrics(),
			consumererror.NewPermanent(fmt.Errorf("%w: missing org_id in context", ErrUnauthenticated))
	}

	rms := md.ResourceMetrics()
	for i := 0; i < rms.Len(); i++ {
		rm := rms.At(i)
		ra := rm.Resource().Attributes()
		ra.PutStr("device_id", devID)
		ra.PutStr("org_id", orgID)

		sms := rm.ScopeMetrics()
		for j := 0; j < sms.Len(); j++ {
			ms := sms.At(j).Metrics()
			for k := 0; k < ms.Len(); k++ {
				m := ms.At(k)
				switch m.Type() {
				case pmetric.MetricTypeGauge:
					dps := m.Gauge().DataPoints()
					for n := 0; n < dps.Len(); n++ {
						dp := dps.At(n)
						dp.Attributes().PutStr("device_id", devID)
						dp.Attributes().PutStr("org_id", orgID)
					}
				case pmetric.MetricTypeSum:
					dps := m.Sum().DataPoints()
					for n := 0; n < dps.Len(); n++ {
						dp := dps.At(n)
						dp.Attributes().PutStr("device_id", devID)
						dp.Attributes().PutStr("org_id", orgID)
					}
				case pmetric.MetricTypeHistogram:
					dps := m.Histogram().DataPoints()
					for n := 0; n < dps.Len(); n++ {
						dp := dps.At(n)
						dp.Attributes().PutStr("device_id", devID)
						dp.Attributes().PutStr("org_id", orgID)
					}
				case pmetric.MetricTypeExponentialHistogram:
					dps := m.ExponentialHistogram().DataPoints()
					for n := 0; n < dps.Len(); n++ {
						dp := dps.At(n)
						dp.Attributes().PutStr("device_id", devID)
						dp.Attributes().PutStr("org_id", orgID)
					}
				case pmetric.MetricTypeSummary:
					dps := m.Summary().DataPoints()
					for n := 0; n < dps.Len(); n++ {
						dp := dps.At(n)
						dp.Attributes().PutStr("device_id", devID)
						dp.Attributes().PutStr("org_id", orgID)
					}
				}
			}
		}
	}
	return md, nil
}
