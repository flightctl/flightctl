package firmwarepolicy_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	receiver "github.com/flightctl/flightctl/internal/agent/device/firmwarepolicy"
	generatedclient "github.com/flightctl/flightctl/internal/api/client/agent"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/flightctl/flightctl/test/util/firmwarecatalog"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func views(t *testing.T) (policy.View, policy.View, policy.Binding) {
	t.Helper()
	cfg, err := firmwarecatalog.New()
	require.NoError(t, err)
	data, err := json.Marshal(cfg.Releases[0])
	require.NoError(t, err)
	b := policy.Binding{Name: cfg.Releases[0].Name, Version: cfg.Releases[0].Version, DescriptorDigest: policy.Hash(data), Descriptor: data}
	one, err := policy.New(1, []policy.Binding{b}, nil)
	require.NoError(t, err)
	two, err := policy.New(2, []policy.Binding{b}, []policy.Tombstone{{Name: b.Name, Version: b.Version, DescriptorDigest: b.DescriptorDigest, Reason: "revoked", Revision: 2}})
	require.NoError(t, err)
	return one, two, b
}

type network struct {
	client *client.MockManagement
	views  chan policy.View
	acks   chan policy.Receipt
}

func newNetwork(t *testing.T) *network {
	t.Helper()
	ctrl := gomock.NewController(t)
	n := &network{client: client.NewMockManagement(ctrl), views: make(chan policy.View, 1), acks: make(chan policy.Receipt, 20)}
	n.client.EXPECT().GetFirmwarePolicy(gomock.Any(), "device", gomock.Any()).DoAndReturn(func(ctx context.Context, _ string, _ policy.Receipt, _ ...any) (*policy.View, int, error) {
		select {
		case v := <-n.views:
			return &v, 200, nil
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}).AnyTimes()
	n.client.EXPECT().AcknowledgeFirmwarePolicy(gomock.Any(), "device", gomock.Any()).DoAndReturn(func(_ context.Context, _ string, r policy.Receipt, _ ...any) (int, error) {
		n.acks <- r
		return 204, nil
	}).AnyTimes()
	return n
}
func manager(t *testing.T, dir string, rw fileio.ReadWriter, n *network) *receiver.Manager {
	t.Helper()
	m, err := receiver.New("device", dir, rw, n.client, log.NewPrefixLogger("policy-test"), receiver.WithPollConfig(poll.Config{BaseDelay: 5 * time.Millisecond, Factor: 1, MaxDelay: 10 * time.Millisecond}))
	require.NoError(t, err)
	return m
}
func run(t *testing.T, m *receiver.Manager) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("policy poller did not stop")
		}
	})
	return ctx
}
func accept(t *testing.T, ctx context.Context, m *receiver.Manager, revision int64) {
	t.Helper()
	require.Eventually(t, func() bool {
		if m.Sync(ctx) != nil {
			return false
		}
		v, err := m.Current()
		return err == nil && v != nil && v.Revision == revision
	}, time.Second, 5*time.Millisecond)
}

func TestPolicyReceiptRetention(t *testing.T) {
	for _, revokedFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "When approval precedes revocation it should retain both in order", true: "When revocation precedes first receipt it should never approve the release"}[revokedFirst], func(t *testing.T) {
			one, two, b := views(t)
			dir := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
			n := newNetwork(t)
			m := manager(t, dir, rw, n)
			ctx := run(t, m)
			for _, name := range []string{"current.json", "desired.json", "rollback.json", "firmware/attempt-fixture.json"} {
				require.NoError(t, rw.WriteFile(filepath.Join(dir, name), []byte("pinned control"), 0600))
			}
			if revokedFirst {
				n.views <- two
			} else {
				n.views <- one
			}
			require.Never(t, func() bool { return len(n.acks) > 0 }, 30*time.Millisecond, 5*time.Millisecond)
			require.False(t, m.IsApproved(b.Name, b.Version, b.DescriptorDigest))
			if !revokedFirst {
				accept(t, ctx, m, 1)
				require.True(t, m.IsApproved(b.Name, b.Version, b.DescriptorDigest))
				require.False(t, m.IsApproved(b.Name, b.Version, b.DescriptorDigest+"x"))
				n.views <- two
			}
			accept(t, ctx, m, 2)
			require.False(t, m.IsApproved(b.Name, b.Version, b.DescriptorDigest))
			require.Eventually(t, func() bool {
				select {
				case ack := <-n.acks:
					return ack.Revision == 2 && ack.Digest == two.Digest
				default:
					return false
				}
			}, time.Second, 5*time.Millisecond)
			retained, err := rw.ReadFileLimited(filepath.Join(dir, "firmware/policy.json"), policy.MaxWireBytes)
			require.NoError(t, err)
			view, err := policy.ParseView(retained)
			require.NoError(t, err)
			require.Equal(t, two, view)
			restart := manager(t, dir, rw, newNetwork(t))
			got, err := restart.Current()
			require.NoError(t, err)
			require.Equal(t, two, *got)
			require.False(t, restart.IsApproved(b.Name, b.Version, b.DescriptorDigest))
			n.views <- one
			require.Eventually(t, func() bool { return m.Sync(ctx) != nil }, time.Second, 5*time.Millisecond)
			got, err = m.Current()
			require.NoError(t, err)
			require.Equal(t, two, *got)
			for _, name := range []string{"current.json", "desired.json", "rollback.json", "firmware/attempt-fixture.json"} {
				data, err := rw.ReadFile(filepath.Join(dir, name))
				require.NoError(t, err)
				require.Equal(t, "pinned control", string(data))
			}
		})
	}
}

func TestPolicyDurabilityFailure(t *testing.T) {
	one, two, b := views(t)
	ctrl := gomock.NewController(t)
	rw := fileio.NewMockReadWriter(ctrl)
	initial, err := json.Marshal(one)
	require.NoError(t, err)
	rw.EXPECT().ReadFileLimited("/data/firmware/policy.json", int64(policy.MaxWireBytes)).Return(initial, nil)
	first := rw.EXPECT().WriteFileDurable("/data/firmware/policy.json", gomock.Any(), os.FileMode(0700), os.FileMode(0600)).Return(errors.New("directory sync failed"))
	rw.EXPECT().WriteFileDurable("/data/firmware/policy.json", gomock.Any(), os.FileMode(0700), os.FileMode(0600)).Return(nil).After(first)
	n := newNetwork(t)
	m := manager(t, "/data", rw, n)
	ctx := run(t, m)
	require.True(t, m.IsApproved(b.Name, b.Version, b.DescriptorDigest))
	n.views <- two
	require.Eventually(t, func() bool { return m.Sync(ctx) != nil }, time.Second, 5*time.Millisecond)
	require.False(t, m.IsApproved(b.Name, b.Version, b.DescriptorDigest))
	current, err := m.Current()
	require.Error(t, err)
	require.Equal(t, int64(1), current.Revision)
	for len(n.acks) > 0 {
		ack := <-n.acks
		require.NotEqual(t, int64(2), ack.Revision)
	}
	n.views <- one
	require.Eventually(t, func() bool { return m.Sync(ctx) != nil }, time.Second, 5*time.Millisecond)
	require.False(t, m.IsApproved(b.Name, b.Version, b.DescriptorDigest))
	n.views <- two
	accept(t, ctx, m, 2)
	require.False(t, m.IsApproved(b.Name, b.Version, b.DescriptorDigest))
}

func TestPolicyCorruptState(t *testing.T) {
	one, _, b := views(t)
	for _, tc := range []struct {
		name string
		data []byte
		err  error
	}{{"When retained JSON is corrupt it should withhold authorization", []byte("corrupt"), nil}, {"When retained state is unreadable it should withhold authorization", nil, os.ErrPermission}} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			rw := fileio.NewMockReadWriter(ctrl)
			rw.EXPECT().ReadFileLimited("/data/firmware/policy.json", int64(policy.MaxWireBytes)).Return(tc.data, tc.err)
			n := newNetwork(t)
			m := manager(t, "/data", rw, n)
			_, err := m.Current()
			require.Error(t, err)
			require.False(t, m.IsApproved(b.Name, b.Version, b.DescriptorDigest))
			ctx := run(t, m)
			n.views <- one
			require.Eventually(t, func() bool { return m.Sync(ctx) != nil }, time.Second, 5*time.Millisecond)
			require.Empty(t, n.acks)
		})
	}
}

func TestPolicyAckFailureDoesNotSuppressRevocation(t *testing.T) {
	one, two, b := views(t)
	dir := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	data, err := json.Marshal(one)
	require.NoError(t, err)
	require.NoError(t, rw.WriteFileDurable(filepath.Join(dir, "firmware/policy.json"), data, 0700, 0600))
	ctrl := gomock.NewController(t)
	management := client.NewMockManagement(ctrl)
	management.EXPECT().AcknowledgeFirmwarePolicy(gomock.Any(), "device", gomock.Any()).Return(503, nil).AnyTimes()
	first := management.EXPECT().GetFirmwarePolicy(gomock.Any(), "device", gomock.Any()).Return(&two, 200, nil)
	management.EXPECT().GetFirmwarePolicy(gomock.Any(), "device", gomock.Any()).DoAndReturn(func(ctx context.Context, _ string, _ policy.Receipt, _ ...generatedclient.RequestEditorFn) (*policy.View, int, error) {
		<-ctx.Done()
		return nil, 0, ctx.Err()
	}).After(first).AnyTimes()
	m, err := receiver.New("device", dir, rw, management, log.NewPrefixLogger("ack-failure"), receiver.WithPollConfig(poll.Config{BaseDelay: time.Millisecond, Factor: 1, MaxDelay: time.Millisecond}))
	require.NoError(t, err)
	require.True(t, m.IsApproved(b.Name, b.Version, b.DescriptorDigest))
	ctx := run(t, m)
	accept(t, ctx, m, 2)
	require.False(t, m.IsApproved(b.Name, b.Version, b.DescriptorDigest))
}

func TestPolicyConstructorValidation(t *testing.T) {
	rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	n := newNetwork(t)
	logger := log.NewPrefixLogger("validation")
	for _, tc := range []struct {
		name, dir string
		rw        fileio.ReadWriter
		client    client.Management
		log       *log.PrefixLogger
	}{{"", "/data", rw, n.client, logger}, {"device", "relative", rw, n.client, logger}, {"device", "/data", nil, n.client, logger}, {"device", "/data", rw, nil, logger}, {"device", "/data", rw, n.client, nil}} {
		_, err := receiver.New(tc.name, tc.dir, tc.rw, tc.client, tc.log)
		require.Error(t, err)
	}
	_, err := receiver.New("device", t.TempDir(), rw, n.client, logger, receiver.WithPollConfig(poll.Config{}))
	require.Error(t, err)
}

func TestPolicyOfflineRetention(t *testing.T) {
	one, _, b := views(t)
	dir := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	data, err := json.Marshal(one)
	require.NoError(t, err)
	require.NoError(t, rw.WriteFileDurable(filepath.Join(dir, "firmware/policy.json"), data, 0700, 0600))
	ctrl := gomock.NewController(t)
	management := client.NewMockManagement(ctrl)
	observed := make(chan struct{}, 1)
	management.EXPECT().AcknowledgeFirmwarePolicy(gomock.Any(), "device", gomock.Any()).Return(503, nil).AnyTimes()
	management.EXPECT().GetFirmwarePolicy(gomock.Any(), "device", gomock.Any()).DoAndReturn(func(context.Context, string, policy.Receipt, ...generatedclient.RequestEditorFn) (*policy.View, int, error) {
		select {
		case observed <- struct{}{}:
		default:
		}
		return nil, 503, nil
	}).AnyTimes()
	m, err := receiver.New("device", dir, rw, management, log.NewPrefixLogger("offline"), receiver.WithPollConfig(poll.Config{BaseDelay: time.Millisecond, Factor: 1, MaxDelay: time.Millisecond}))
	require.NoError(t, err)
	run(t, m)
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("offline fetch was not attempted")
	}
	retained, err := m.Current()
	require.NoError(t, err)
	require.Equal(t, one, *retained)
	require.True(t, m.IsApproved(b.Name, b.Version, b.DescriptorDigest))
	persisted, err := rw.ReadFileLimited(filepath.Join(dir, "firmware/policy.json"), policy.MaxWireBytes)
	require.NoError(t, err)
	require.Equal(t, data, persisted)
}
