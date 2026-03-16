import re

with open("internal/agent/device/applications/podman_monitor_test.go", "r") as f:
    content = f.read()

new_test = """
		{
			name: "single app single container manual stop",
			apps: []Application{
				createTestApplication(require, "app1", v1beta1.ApplicationStatusPreparing, v1beta1.CurrentProcessUsername),
			},
			events: []client.PodmanEvent{
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "init"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "create"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "start"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "stop"),
				mockPodmanEventSuccess("app1", v1beta1.CurrentProcessUsername, "app1-service-1", "die"),
			},
			expectedReady:    "0/1",
			expectedStatus:   v1beta1.ApplicationStatusError,
			expectedSummary:  v1beta1.ApplicationsSummaryStatusError,
			expectedRestarts: 0,
		},
"""

content = content.replace('name: "single app start then die",', new_test.strip() + '\n\t\t},\n\t\t{\n\t\t\tname: "single app start then die",')

with open("internal/agent/device/applications/podman_monitor_test.go", "w") as f:
    f.write(content)
