package device

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	receiver "github.com/flightctl/flightctl/internal/agent/device/firmwarepolicy"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	generatedclient "github.com/flightctl/flightctl/internal/api/client/agent"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/flightctl/flightctl/test/util/firmwarecatalog"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestAgentAcceptsPolicyWithInvalidDesiredState(t *testing.T) {
	ctrl := gomock.NewController(t)
	management := client.NewMockManagement(ctrl)
	specs := spec.NewMockManager(ctrl)
	statuses := status.NewMockManager(ctrl)
	cfg, err := firmwarecatalog.New()
	require.NoError(t, err)
	raw, err := json.Marshal(cfg.Releases[0])
	require.NoError(t, err)
	binding := policy.Binding{Name: cfg.Releases[0].Name, Version: cfg.Releases[0].Version, Descriptor: raw, DescriptorDigest: policy.Hash(raw)}
	view, err := policy.New(1, []policy.Binding{binding}, nil)
	require.NoError(t, err)
	first := management.EXPECT().GetFirmwarePolicy(gomock.Any(), "device", gomock.Any()).Return(&view, 200, nil)
	management.EXPECT().GetFirmwarePolicy(gomock.Any(), "device", gomock.Any()).DoAndReturn(func(ctx context.Context, _ string, _ policy.Receipt, _ ...generatedclient.RequestEditorFn) (*policy.View, int, error) {
		<-ctx.Done()
		return nil, 0, ctx.Err()
	}).After(first).AnyTimes()
	management.EXPECT().AcknowledgeFirmwarePolicy(gomock.Any(), "device", gomock.Any()).Return(204, nil).AnyTimes()
	rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	receiver, err := receiver.New("device", t.TempDir(), rw, management, log.NewPrefixLogger("test"), receiver.WithPollConfig(poll.Config{BaseDelay: time.Millisecond, Factor: 1, MaxDelay: time.Millisecond}))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { receiver.Run(ctx); close(done) }()
	specs.EXPECT().GetDesired(gomock.Any()).DoAndReturn(func(context.Context) (*api.Device, bool, error) {
		if receiver.IsApproved(binding.Name, binding.Version, binding.DescriptorDigest) {
			cancel()
		}
		return nil, false, errors.New("desired firmware is invalid")
	}).AnyTimes()
	statuses.EXPECT().Sync(gomock.Any()).Return(nil).AnyTimes()
	agent := &Agent{specManager: specs, statusManager: statuses, statusUpdateInterval: util.Duration(time.Second), log: log.NewPrefixLogger("test")}
	WithFirmwarePolicyReceiver(receiver)(agent)
	require.NoError(t, agent.Run(ctx))
	require.True(t, receiver.IsApproved(binding.Name, binding.Version, binding.DescriptorDigest))
	<-done
}
