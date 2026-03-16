#!/bin/bash
sed -i '/name: "single app multiple containers started then one manual stop result sigkill",/i \
                {\
                        name: "single app multiple containers started then all manual stop exit code 0",\
                        apps: []Application{\
                                createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),\
                        },\
                        events: []client.PodmanEvent{\
                                mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "init"),\
                                mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "create"),\
                                mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "start"),\
                                mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "init"),\
                                mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "create"),\
                                mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "start"),\
                                mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "stop"),\
                                mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "die"),\
                                mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "stop"),\
                                mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-2", "die"),\
                        },\
                        expectedReady:   "0/2",\
                        expectedStatus:  v1beta1.ApplicationStatusError,\
                        expectedSummary: v1beta1.ApplicationsSummaryStatusError,\
                },' internal/agent/device/applications/podman_monitor_test.go
