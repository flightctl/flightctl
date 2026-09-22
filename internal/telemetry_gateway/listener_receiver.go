package telemetrygateway

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componentstatus"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/otlpreceiver"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const otlpDataFormatProtobuf = "protobuf"

// newListenerAwareOTLPFactory creates the OTLP receiver used by the telemetry
// gateway. listenerReady is optional; when set, it reports the address after
// the receiver binds its listener.
func newListenerAwareOTLPFactory(listenerReady func(string)) receiver.Factory {
	standardFactory := otlpreceiver.NewFactory()
	return receiver.NewFactory(
		component.MustNewType("otlp"),
		standardFactory.CreateDefaultConfig,
		receiver.WithMetrics(func(
			ctx context.Context,
			set receiver.Settings,
			cfg component.Config,
			next consumer.Metrics,
		) (receiver.Metrics, error) {
			otlpCfg, ok := cfg.(*otlpreceiver.Config)
			if !ok {
				return nil, fmt.Errorf("unexpected OTLP receiver config type %T", cfg)
			}
			return newListenerAwareMetricsReceiver(ctx, set, otlpCfg, next, listenerReady)
		}, component.StabilityLevelAlpha),
	)
}

type listenerAwareMetricsReceiver struct {
	config        *otlpreceiver.Config
	settings      receiver.Settings
	nextConsumer  consumer.Metrics
	obsreport     *receiverhelper.ObsReport
	listenerReady func(string)

	server   *grpc.Server
	shutdown sync.WaitGroup
}

func newListenerAwareMetricsReceiver(
	_ context.Context,
	set receiver.Settings,
	cfg *otlpreceiver.Config,
	next consumer.Metrics,
	listenerReady func(string),
) (receiver.Metrics, error) {
	obsreport, err := receiverhelper.NewObsReport(receiverhelper.ObsReportSettings{
		ReceiverID:             set.ID,
		Transport:              "grpc",
		ReceiverCreateSettings: set,
	})
	if err != nil {
		return nil, err
	}

	return &listenerAwareMetricsReceiver{
		config:        cfg,
		settings:      set,
		nextConsumer:  next,
		obsreport:     obsreport,
		listenerReady: listenerReady,
	}, nil
}

func (r *listenerAwareMetricsReceiver) Start(ctx context.Context, host component.Host) error {
	if !r.config.GRPC.HasValue() {
		return errors.New("OTLP gRPC protocol is required")
	}

	grpcCfg := r.config.GRPC.Get()
	server, err := grpcCfg.ToServer(ctx, host, r.settings.TelemetrySettings)
	if err != nil {
		return err
	}

	listener, err := grpcCfg.NetAddr.Listen(ctx)
	if err != nil {
		return err
	}

	pmetricotlp.RegisterGRPCServer(server, &listenerAwareMetricsServer{
		nextConsumer: r.nextConsumer,
		obsreport:    r.obsreport,
	})
	r.server = server

	r.shutdown.Add(1)
	go func() {
		defer r.shutdown.Done()
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
			componentstatus.ReportStatus(host, componentstatus.NewFatalErrorEvent(serveErr))
		}
	}()

	if r.listenerReady != nil {
		r.listenerReady(listener.Addr().String())
	}
	return nil
}

func (r *listenerAwareMetricsReceiver) Shutdown(_ context.Context) error {
	if r.server != nil {
		r.server.GracefulStop()
	}
	r.shutdown.Wait()
	return nil
}

type listenerAwareMetricsServer struct {
	pmetricotlp.UnimplementedGRPCServer
	nextConsumer consumer.Metrics
	obsreport    *receiverhelper.ObsReport
}

func (r *listenerAwareMetricsServer) Export(
	ctx context.Context,
	req pmetricotlp.ExportRequest,
) (pmetricotlp.ExportResponse, error) {
	metrics := req.Metrics()
	dataPointCount := metrics.DataPointCount()
	if dataPointCount == 0 {
		return pmetricotlp.NewExportResponse(), nil
	}

	ctx = r.obsreport.StartMetricsOp(ctx)
	err := r.nextConsumer.ConsumeMetrics(ctx, metrics)
	r.obsreport.EndMetricsOp(ctx, otlpDataFormatProtobuf, dataPointCount, err)
	if err != nil {
		return pmetricotlp.NewExportResponse(), status.Error(codes.Unavailable, err.Error())
	}
	return pmetricotlp.NewExportResponse(), nil
}

var _ receiver.Metrics = (*listenerAwareMetricsReceiver)(nil)
