package main

import (
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/flightctl/flightctl/pkg/catalogcollector/destination/debugdestination"
	"github.com/flightctl/flightctl/pkg/catalogcollector/destination/flightctldestination"
	"github.com/flightctl/flightctl/pkg/catalogcollector/extension/bearertokenauthextension"
	"github.com/flightctl/flightctl/pkg/catalogcollector/extension/healthcheckextension"
	"github.com/flightctl/flightctl/pkg/catalogcollector/extension/oauth2clientauthextension"
	"github.com/flightctl/flightctl/pkg/catalogcollector/processor/catalognameprocessor"
	"github.com/flightctl/flightctl/pkg/catalogcollector/source/httpsource"
	"github.com/flightctl/flightctl/pkg/catalogcollector/source/kubeflowmodelregistrysource"
)

// components returns the set of source, processor, and destination factories
// available to the official catalog collector distribution.
//
// This is the explicit composition point: real built-in components are
// imported and registered here. External repositories that compile custom
// collector distributions provide their own components() function that
// merges additional factories with the built-in set.
//
// There is no global registration, init()-time side effect, or package-level
// registry. All factory wiring is compile-time and explicit.
func components() catalogcollector.Factories {
	return catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			httpsource.NewFactory(),
			kubeflowmodelregistrysource.NewFactory(),
		},
		Processors: []catalogcollector.ProcessorFactory{
			catalognameprocessor.NewFactory(),
		},
		Destinations: []catalogcollector.DestinationFactory{
			debugdestination.NewFactory(),
			flightctldestination.NewFactory(),
		},
		Extensions: []catalogcollector.ExtensionFactory{
			healthcheckextension.NewFactory(),
			bearertokenauthextension.NewFactory(),
			oauth2clientauthextension.NewFactory(),
		},
	}
}
