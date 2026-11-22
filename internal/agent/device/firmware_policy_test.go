package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
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
	updates := spec.NewMockWatcher(ctrl)
	updates.EXPECT().TryPop().Return(nil, false, nil).AnyTimes()
	specs.EXPECT().Read(gomock.Any()).Return(nil, os.ErrNotExist).AnyTimes()
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
	WithFirmwarePolicyReceiver(receiver, updates)(agent)
	receiver.RequirePolicy()
	require.NoError(t, agent.Run(ctx))
	require.True(t, receiver.IsApproved(binding.Name, binding.Version, binding.DescriptorDigest))
	<-done
}

func TestAgentStartsPolicyDeliveryForFirmwareWork(t *testing.T) {
	target := &api.Device{Spec: &api.DeviceSpec{Firmware: &api.FirmwareSpec{}}}
	attempt := &api.Device{Status: &api.DeviceStatus{Firmware: &api.FirmwareStatus{Attempt: &api.FirmwareAttemptStatus{}}}}
	for _, tc := range []struct {
		name     string
		kind     spec.Type
		retained *api.Device
		readErr  error
		incoming *api.Device
		active   bool
	}{
		{name: "no firmware work"},
		{name: "support reporting only", incoming: &api.Device{Status: &api.DeviceStatus{Firmware: &api.FirmwareStatus{}}}},
		{name: "current firmware", kind: spec.Current, retained: target, active: true},
		{name: "desired firmware", kind: spec.Desired, retained: target, active: true},
		{name: "rollback firmware", kind: spec.Rollback, retained: target, active: true},
		{name: "retained attempt", kind: spec.Current, retained: attempt, active: true},
		{name: "unreadable retained target", kind: spec.Current, readErr: os.ErrPermission, active: true},
		{name: "new firmware behind a maintenance window", incoming: target, active: true},
	} {
		t.Run("When there is "+tc.name+" it should select policy delivery safely", func(t *testing.T) {
			ctrl := gomock.NewController(t)
			management := client.NewMockManagement(ctrl)
			var calls atomic.Int64
			management.EXPECT().GetFirmwarePolicy(gomock.Any(), "device", gomock.Any()).DoAndReturn(func(context.Context, string, policy.Receipt, ...generatedclient.RequestEditorFn) (*policy.View, int, error) {
				calls.Add(1)
				return nil, 204, nil
			}).AnyTimes()
			rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
			m, err := receiver.New("device", t.TempDir(), rw, management, log.NewPrefixLogger("activation"), receiver.WithPollConfig(poll.Config{BaseDelay: time.Millisecond, Factor: 1, MaxDelay: time.Millisecond}))
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { m.Run(ctx); close(done) }()
			defer func() { cancel(); <-done }()
			specs := spec.NewMockManager(ctrl)
			for _, kind := range []spec.Type{spec.Current, spec.Desired, spec.Rollback} {
				if kind == tc.kind {
					specs.EXPECT().Read(kind).Return(tc.retained, tc.readErr)
				} else {
					specs.EXPECT().Read(kind).Return(nil, fmt.Errorf("read target: %w", os.ErrNotExist))
				}
			}
			// The desired-state policy queue remains blocked throughout the test.
			specs.EXPECT().GetDesired(gomock.Any()).Return(nil, true, nil).AnyTimes()
			updates := spec.NewMockWatcher(ctrl)
			if tc.incoming != nil {
				updates.EXPECT().TryPop().Return(tc.incoming, true, nil)
			}
			updates.EXPECT().TryPop().Return(nil, false, nil).AnyTimes()
			agent := &Agent{specManager: specs, log: log.NewPrefixLogger("activation")}
			WithFirmwarePolicyReceiver(m, updates)(agent)
			agent.syncDeviceSpec(ctx)
			if tc.active {
				require.Eventually(t, func() bool { return calls.Load() > 0 }, time.Second, time.Millisecond)
				before := calls.Load()
				// A missing subsequent target must not stop revocation delivery.
				agent.syncDeviceSpec(ctx)
				require.Eventually(t, func() bool { return calls.Load() > before }, time.Second, time.Millisecond)
			} else {
				require.Never(t, func() bool { return calls.Load() > 0 }, 30*time.Millisecond, time.Millisecond)
			}
		})
	}
}

func TestPolicyRevocationWhileDeviceReconciliationIsBlocked(t *testing.T) {
	ctrl := gomock.NewController(t)
	management := client.NewMockManagement(ctrl)
	specs := spec.NewMockManager(ctrl)
	statuses := status.NewMockManager(ctrl)
	cfg, err := firmwarecatalog.New()
	require.NoError(t, err)
	raw, err := json.Marshal(cfg.Releases[0])
	require.NoError(t, err)
	binding := policy.Binding{Name: cfg.Releases[0].Name, Version: cfg.Releases[0].Version, Descriptor: raw, DescriptorDigest: policy.Hash(raw)}
	approval, err := policy.New(1, []policy.Binding{binding}, nil)
	require.NoError(t, err)
	revocation, err := policy.New(2, []policy.Binding{binding}, []policy.Tombstone{{Name: binding.Name, Version: binding.Version, DescriptorDigest: binding.DescriptorDigest, Revision: 2, Reason: "revoked"}})
	require.NoError(t, err)
	rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	dir := t.TempDir()
	data, err := json.Marshal(approval)
	require.NoError(t, err)
	require.NoError(t, rw.WriteFileDurable(filepath.Join(dir, "firmware/policy.json"), data, 0700, 0600))
	blocked := make(chan struct{})
	subsequentFetch := make(chan struct{})
	acknowledged := make(chan struct{}, 1)
	specs.EXPECT().Read(gomock.Any()).Return(nil, os.ErrNotExist).AnyTimes()
	specs.EXPECT().GetDesired(gomock.Any()).DoAndReturn(func(ctx context.Context) (*api.Device, bool, error) {
		close(blocked)
		<-ctx.Done()
		return nil, true, nil
	})
	statuses.EXPECT().Sync(gomock.Any()).Return(nil).AnyTimes()
	first := management.EXPECT().GetFirmwarePolicy(gomock.Any(), "device", gomock.Any()).DoAndReturn(func(ctx context.Context, _ string, _ policy.Receipt, _ ...generatedclient.RequestEditorFn) (*policy.View, int, error) {
		select {
		case <-blocked:
			return &revocation, 200, nil
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	})
	management.EXPECT().GetFirmwarePolicy(gomock.Any(), "device", policy.Receipt{Revision: 2, Digest: revocation.Digest}).DoAndReturn(func(ctx context.Context, _ string, _ policy.Receipt, _ ...generatedclient.RequestEditorFn) (*policy.View, int, error) {
		close(subsequentFetch)
		<-ctx.Done()
		return nil, 0, ctx.Err()
	}).After(first)
	management.EXPECT().AcknowledgeFirmwarePolicy(gomock.Any(), "device", gomock.Any()).DoAndReturn(func(_ context.Context, _ string, receipt policy.Receipt, _ ...generatedclient.RequestEditorFn) (int, error) {
		if receipt.Revision == 2 {
			acknowledged <- struct{}{}
		}
		return 204, nil
	}).AnyTimes()
	m, err := receiver.New("device", dir, rw, management, log.NewPrefixLogger("blocked-update"), receiver.WithPollConfig(poll.Config{BaseDelay: time.Millisecond, Factor: 1, MaxDelay: time.Millisecond}))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	pollDone, agentDone := make(chan struct{}), make(chan struct{})
	agent := &Agent{specManager: specs, statusManager: statuses, statusUpdateInterval: util.Duration(time.Second), log: log.NewPrefixLogger("blocked-update")}
	WithFirmwarePolicyReceiver(m, nil)(agent)
	go func() { defer close(pollDone); m.Run(ctx) }()
	go func() { defer close(agentDone); _ = agent.Run(ctx) }()
	defer func() { cancel(); <-pollDone; <-agentDone }()
	require.Eventually(t, func() bool { return !m.IsApproved(binding.Name, binding.Version, binding.DescriptorDigest) }, time.Second, time.Millisecond)
	select {
	case <-acknowledged:
	case <-time.After(time.Second):
		t.Fatal("revocation was not acknowledged during blocked reconciliation")
	}
	select {
	case <-subsequentFetch:
	case <-time.After(time.Second):
		t.Fatal("later policy fetch was blocked by reconciliation")
	}
	select {
	case <-agentDone:
		t.Fatal("reconciliation unexpectedly finished")
	default:
	}
}
