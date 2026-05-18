package tasks

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func collectMetrics(c prometheus.Collector) []*dto.MetricFamily {
	ch := make(chan prometheus.Metric, 100)
	go func() {
		c.Collect(ch)
		close(ch)
	}()

	var families []*dto.MetricFamily
	for m := range ch {
		d := m.Desc()
		pb := &dto.Metric{}
		_ = m.Write(pb)
		_ = d
		families = append(families, &dto.MetricFamily{Metric: []*dto.Metric{pb}})
	}
	return families
}

func collectDescs(c prometheus.Collector) []*prometheus.Desc {
	ch := make(chan *prometheus.Desc, 100)
	go func() {
		c.Describe(ch)
		close(ch)
	}()
	var descs []*prometheus.Desc
	for d := range ch {
		descs = append(descs, d)
	}
	return descs
}

func TestDependencySyncCollector_Describe(t *testing.T) {
	t.Run("When Describe is called it should output all expected metric descriptors", func(t *testing.T) {
		c := NewDependencySyncCollector()
		descs := collectDescs(c)
		require.GreaterOrEqual(t, len(descs), 5, "expected at least 5 descriptors (3 counters + 1 histogram + 1 gauge)")
	})
}

func TestDependencySyncCollector_Collect(t *testing.T) {
	t.Run("When Collect is called it should output all registered metrics", func(t *testing.T) {
		c := NewDependencySyncCollector()
		metrics := collectMetrics(c)
		require.NotEmpty(t, metrics)
	})
}

func TestDependencySyncCollector_ObserveProbeCycle(t *testing.T) {
	tests := []struct {
		name    string
		refType string
		count   int
	}{
		{
			name:    "When git probe cycle is observed it should increment the counter",
			refType: RefTypeGit,
			count:   3,
		},
		{
			name:    "When http probe cycle is observed it should increment the counter",
			refType: RefTypeHTTP,
			count:   1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewDependencySyncCollector()
			for i := 0; i < tc.count; i++ {
				c.ObserveProbeCycle(tc.refType)
			}
			val := getCounterValue(t, c.cyclesTotal, tc.refType)
			require.Equal(t, float64(tc.count), val)
		})
	}
}

func TestDependencySyncCollector_ObserveProbeChange(t *testing.T) {
	t.Run("When a change is observed it should increment the changes counter", func(t *testing.T) {
		c := NewDependencySyncCollector()
		c.ObserveProbeChange(RefTypeGit)
		c.ObserveProbeChange(RefTypeGit)
		val := getCounterValue(t, c.changesTotal, RefTypeGit)
		require.Equal(t, float64(2), val)
	})
}

func TestDependencySyncCollector_ObserveProbeError(t *testing.T) {
	t.Run("When a probe error is observed it should increment the errors counter", func(t *testing.T) {
		c := NewDependencySyncCollector()
		c.ObserveProbeError(RefTypeHTTP)
		val := getCounterValue(t, c.probeErrorsTotal, RefTypeHTTP)
		require.Equal(t, float64(1), val)
	})
}

func TestDependencySyncCollector_ObserveProbeLatency(t *testing.T) {
	t.Run("When probe latency is observed it should record to the histogram", func(t *testing.T) {
		c := NewDependencySyncCollector()
		c.ObserveProbeLatency(RefTypeGit, 150*time.Millisecond)
		c.ObserveProbeLatency(RefTypeGit, 250*time.Millisecond)

		ch := make(chan prometheus.Metric, 10)
		c.probeLatency.Collect(ch)
		close(ch)

		found := false
		for m := range ch {
			pb := &dto.Metric{}
			require.NoError(t, m.Write(pb))
			if pb.Histogram != nil && pb.Histogram.GetSampleCount() == 2 {
				found = true
			}
		}
		require.True(t, found, "expected histogram with 2 samples for ref_type=git")
	})
}

func TestDependencySyncCollector_SetInformerConnected(t *testing.T) {
	tests := []struct {
		name      string
		connected bool
		expected  float64
	}{
		{
			name:      "When informer is connected it should set gauge to 1",
			connected: true,
			expected:  1,
		},
		{
			name:      "When informer is disconnected it should set gauge to 0",
			connected: false,
			expected:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewDependencySyncCollector()
			c.SetInformerConnected(tc.connected)

			pb := &dto.Metric{}
			require.NoError(t, c.informerConnected.Write(pb))
			require.NotNil(t, pb.Gauge)
			require.Equal(t, tc.expected, pb.Gauge.GetValue())
		})
	}
}

func getCounterValue(t *testing.T, counterVec *prometheus.CounterVec, label string) float64 {
	t.Helper()
	pb := &dto.Metric{}
	require.NoError(t, counterVec.WithLabelValues(label).Write(pb))
	require.NotNil(t, pb.Counter)
	return pb.Counter.GetValue()
}
