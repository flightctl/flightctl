#!/bin/bash
cd internal/agent/device/applications
go test -run TestPodmanMonitor_updateApplicationStatus_compose_apps
