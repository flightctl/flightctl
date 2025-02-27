package server

import agent "github.com/flightctl/flightctl/api/v1alpha1/agent"

// Service is a wrapper around the generated server interface.
type Service interface {
	StrictServerInterface
}

type DeviceCommands = agent.DeviceCommands
type DeviceCommand = agent.DeviceCommand
type UploadSosReportCommand = agent.UploadSosReportCommand
