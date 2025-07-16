package main

import (
	prometheusexporter "github.com/open-telemetry/opentelemetry-collector-contrib/exporter/prometheusexporter"
	transformprocessor "github.com/open-telemetry/opentelemetry-collector-contrib/processor/transformprocessor"
	"go.opentelemetry.io/collector/cmd/builder/cnauthenticator"
	"go.opentelemetry.io/collector/cmd/builder/deviceidprocessor"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/exporter"
	debugexporter "go.opentelemetry.io/collector/exporter/debugexporter"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/otelcol"
	"go.opentelemetry.io/collector/processor"
	batchprocessor "go.opentelemetry.io/collector/processor/batchprocessor"
	"go.opentelemetry.io/collector/receiver"
	otlpreceiver "go.opentelemetry.io/collector/receiver/otlpreceiver"
)

func components() (otelcol.Factories, error) {
	var err error
	factories := otelcol.Factories{}

	factories.Extensions, err = extension.MakeFactoryMap(
		cnauthenticator.NewFactory(),
	)
	if err != nil {
		return otelcol.Factories{}, err
	}
	factories.ExtensionModules = make(map[component.Type]string, len(factories.Extensions))
	factories.ExtensionModules[cnauthenticator.NewFactory().Type()] = "github.com/example/cnauthenticator v0.0.0"

	factories.Receivers, err = receiver.MakeFactoryMap(
		otlpreceiver.NewFactory(),
	)
	if err != nil {
		return otelcol.Factories{}, err
	}
	factories.ReceiverModules = make(map[component.Type]string, len(factories.Receivers))
	factories.ReceiverModules[otlpreceiver.NewFactory().Type()] = "go.opentelemetry.io/collector/receiver/otlpreceiver v0.110.0"

	factories.Exporters, err = exporter.MakeFactoryMap(
		debugexporter.NewFactory(),
		prometheusexporter.NewFactory(),
	)
	if err != nil {
		return otelcol.Factories{}, err
	}
	factories.ExporterModules = make(map[component.Type]string, len(factories.Exporters))
	factories.ExporterModules[debugexporter.NewFactory().Type()] = "go.opentelemetry.io/collector/exporter/debugexporter v0.110.0"
	factories.ExporterModules[prometheusexporter.NewFactory().Type()] = "go.opentelemetry.io/collector/exporter/prometheus v0.110.0"

	factories.Processors, err = processor.MakeFactoryMap(
		batchprocessor.NewFactory(),
		transformprocessor.NewFactory(),
		deviceidprocessor.NewFactory(),
	)
	if err != nil {
		return otelcol.Factories{}, err
	}
	factories.ProcessorModules = make(map[component.Type]string, len(factories.Processors))
	factories.ProcessorModules[batchprocessor.NewFactory().Type()] = "go.opentelemetry.io/collector/processor/batchprocessor v0.110.0"
	factories.ProcessorModules[transformprocessor.NewFactory().Type()] = "github.com/example/deviceidprocessor v0.0.0"
	factories.ProcessorModules[deviceidprocessor.NewFactory().Type()] = "github.com/example/deviceidprocessor v0.0.0"

	factories.Connectors, err = connector.MakeFactoryMap()
	if err != nil {
		return otelcol.Factories{}, err
	}
	factories.ConnectorModules = make(map[component.Type]string, len(factories.Connectors))

	return factories, nil
}
