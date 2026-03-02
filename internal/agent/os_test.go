// Copyright (c) 2023 Red Hat, Inc.

package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/internal/container/mock_container"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/golang/mock/gomock"
	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"
)

func TestExitedContainerStatus(t *testing.T) {
	require := require.New(t)
	appName := "myApp"
	container1 := "c1"

	containers := map[string]v1alpha1.ContainerSpec{
		container1: {
			Image: "quay.io/flightctl/flightctl:latest",
			Name:  container1,
		},
	}

	testCases := []struct {
		name                string
		podmanStatus        []*container.Status
		podmanError         error
		mockSystemd         bool
		systemdRestart      string
		systemdError        bool
		expectedExitCode    int
		expectError         bool
		expectedErrorString string
	}{
		{
			name:             "Container not exited",
			podmanStatus:     []*container.Status{{Name: container1, State: "running"}},
			expectedExitCode: 0,
			expectError:      true,
		},
		{
			name:             "Podman error",
			podmanError:      fmt.Errorf("podman error"),
			expectedExitCode: 0,
			expectError:      true,
		},
		{
			name:             "Non-zero exit code",
			podmanStatus:     []*container.Status{{Name: container1, State: "exited", ExitCode: 127}},
			expectedExitCode: 127,
			expectError:      false,
		},
		{
			name:             "Zero exit code, restart=no",
			podmanStatus:     []*container.Status{{Name: container1, State: "exited", ExitCode: 0}},
			mockSystemd:      true,
			systemdRestart:   "no",
			expectedExitCode: 0,
			expectError:      false,
		},
		{
			name:             "Zero exit code, restart=on-failure",
			podmanStatus:     []*container.Status{{Name: container1, State: "exited", ExitCode: 0}},
			mockSystemd:      true,
			systemdRestart:   "on-failure",
			expectedExitCode: 1,
			expectError:      false,
		},
		{
			name:             "Zero exit code, restart=always",
			podmanStatus:     []*container.Status{{Name: container1, State: "exited", ExitCode: 0}},
			mockSystemd:      true,
			systemdRestart:   "always",
			expectedExitCode: 1,
			expectError:      false,
		},
		{
			name:             "Systemd error",
			podmanStatus:     []*container.Status{{Name: container1, State: "exited", ExitCode: 0}},
			mockSystemd:      true,
			systemdError:     true,
			expectedExitCode: 1,
			expectError:      false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			podman := mock_container.NewMockPodman(ctrl)
			var systemdConn *dbus.Conn
			if tc.mockSystemd {
				server, err := newMockSystemdDbusServer()
				require.NoError(err)
				defer server.Close()

				unitName := quadlet.GetUnitName(appName, container1)
				objectPath := dbus.ObjectPath("/org/freedesktop/systemd1/unit/" + dbus.ObjectPath.Encode(unitName))

				server.handle = func(m *dbus.Message) {
					if m.Path == objectPath && m.Name == "org.freedesktop.DBus.Properties.Get" {
						if tc.systemdError {
							reply := dbus.NewErrorMessage(m, &dbus.Error{Name: "org.freedesktop.DBus.Error.Failed", Body: []interface{}{"unit not found"}})
							server.conn.Send(reply, nil)
						} else {
							reply, err := dbus.NewMessage(dbus.MsgTypeMethodReturn, dbus.WithSerial(m.Header.Serial))
							require.NoError(err)
							reply.Body = []interface{}{dbus.MakeVariant(tc.systemdRestart)}
							server.conn.Send(reply, nil)
						}
					} else {
						reply := dbus.NewErrorMessage(m, &dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownMethod", Body: []interface{}{"unknown method"}})
						server.conn.Send(reply, nil)
					}
				}
				systemdConn, err = dbus.NewConnection(context.Background(), server.conn)
				require.NoError(err)
				defer systemdConn.Close()
			}

			os := &OS{
				podman:  podman,
				systemd: systemdConn,
			}

			podman.EXPECT().AllContainersStatus(gomock.Any(), appName, gomock.Any()).Return(tc.podmanStatus, tc.podmanError)

			exitCode, err := os.ExitedContainerStatus(context.Background(), appName, containers)

			if tc.expectError {
				require.Error(err)
			} else {
				require.NoError(err)
				require.Equal(tc.expectedExitCode, exitCode)
			}
		})
	}
}
