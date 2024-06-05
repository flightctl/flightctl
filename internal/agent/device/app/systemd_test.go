package app

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestSystemDList(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name          string
		matchPatterns []string
		appCount      int
		result        string
	}{
		{
			name:          "success",
			matchPatterns: []string{"crio.service", "microshift.service"},
			appCount:      2,
			result:        mockSystemDUnitListResult(crioRunning, microshiftRunning),
		},
		{
			name:          "success with no match patterns",
			matchPatterns: nil,
			appCount:      0,
			result:        systemUnitListEmptyResult,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			ctrl := gomock.NewController(t)
			execMock := executer.NewMockExecuter(ctrl)
			defer ctrl.Finish()

			args := append([]string{"list-units", "--all", "--output", "json"}, tt.matchPatterns...)
			execMock.EXPECT().ExecuteWithContext(ctx, SystemdCommand, args).Return(tt.result, "", 0)
			systemd := NewSystemDClient(execMock)

			apps, err := systemd.List(ctx, tt.matchPatterns...)
			require.NoError(err)
			if len(apps) > 0 {
				require.Len(apps, tt.appCount)
			}
		})
	}
}

func TestGetStatus(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name          string
		service       string
		wantState     v1alpha1.ApplicationState
		result        string
		matchPatterns []string
	}{
		{
			name:          "success",
			service:       "crio.service",
			wantState:     v1alpha1.ApplicationStateRunning,
			matchPatterns: []string{"crio.service"},
			result:        mockSystemDUnitListResult(crioRunning),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			ctrl := gomock.NewController(t)
			execMock := executer.NewMockExecuter(ctrl)
			defer ctrl.Finish()

			args := []string{"list-units", "--all", "--output", "json"}
			execMock.EXPECT().ExecuteWithContext(ctx, SystemdCommand, args).Return(tt.result, "", 0)
			systemd := NewSystemDClient(execMock)
			status, err := systemd.GetStatus(ctx, tt.service)
			require.NoError(err)
			require.True(IsState(status, tt.wantState))
			require.Equal(tt.service, *status.Name)
		})
	}
}

const (
	crioRunning = `{
    "unit": "crio.service",
    "load": "loaded",
    "active": "active",
    "sub": "running",
    "description": "cri-o"
  }`

	microshiftRunning = `
  {
    "unit": "microshift.service",
    "load": "loaded",
    "active": "active",
    "sub": "running",
    "description": "MicroShift"
  }`
	systemUnitListEmptyResult = `[]`
)

func mockSystemDUnitListResult(app ...string) string {
	var result string
	for i := 0; i < len(app); i++ {
		result += app[i]
		if i < len(app)-1 {
			result += ","
		}
	}
	return "[" + result + "]"
}
