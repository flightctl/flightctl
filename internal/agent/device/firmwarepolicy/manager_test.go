package firmwarepolicy_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
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
	m.RequirePolicy()
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
			require.False(t, m.IsApproved(b.Name, b.Version, b.DescriptorDigest))
			if revokedFirst {
				n.views <- two
			} else {
				n.views <- one
			}
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
			_, err = m.Receive(ctx, one)
			require.Error(t, err)
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
	_, err = m.Receive(ctx, two)
	require.Error(t, err)
	require.False(t, m.IsApproved(b.Name, b.Version, b.DescriptorDigest))
	current, err := m.Current()
	require.Error(t, err)
	require.Equal(t, int64(1), current.Revision)
	for len(n.acks) > 0 {
		ack := <-n.acks
		require.NotEqual(t, int64(2), ack.Revision)
	}
	_, err = m.Receive(ctx, one)
	require.Error(t, err)
	require.False(t, m.IsApproved(b.Name, b.Version, b.DescriptorDigest))
	n.views <- two
	accept(t, ctx, m, 2)
	require.False(t, m.IsApproved(b.Name, b.Version, b.DescriptorDigest))
}

func TestPolicyCorruptState(t *testing.T) {
	one, _, b := views(t)
	for _, tc := range []struct {
		name    string
		data    []byte
		err     error
		context string
	}{
		{"When retained JSON is corrupt it should withhold authorization", []byte("corrupt"), nil, "decode retained firmware policy"},
		{"When retained state is unreadable it should withhold authorization", nil, os.ErrPermission, "read retained firmware policy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			rw := fileio.NewMockReadWriter(ctrl)
			rw.EXPECT().ReadFileLimited("/data/firmware/policy.json", int64(policy.MaxWireBytes)).Return(tc.data, tc.err)
			n := newNetwork(t)
			m, err := receiver.New("device", "/data", rw, n.client, log.NewPrefixLogger("corrupt-history"))
			require.NoError(t, err)
			_, currentErr := m.Current()
			require.ErrorContains(t, currentErr, tc.context)
			require.False(t, m.IsApproved(b.Name, b.Version, b.DescriptorDigest))
			ctx := run(t, m)
			_, err = m.Receive(ctx, one)
			require.Error(t, err)
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
	require.ErrorContains(t, err, "validate firmware policy retry config")
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

func TestPolicyDeliveryActivation(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(map[bool]string{false: "When firmware is unused it should wait for activation", true: "When policy is retained it should resume delivery without a target"}[retained], func(t *testing.T) {
			one, _, _ := views(t)
			rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
			dir := t.TempDir()
			if retained {
				data, err := json.Marshal(one)
				require.NoError(t, err)
				require.NoError(t, rw.WriteFileDurable(filepath.Join(dir, "firmware/policy.json"), data, 0700, 0600))
			}
			ctrl := gomock.NewController(t)
			management := client.NewMockManagement(ctrl)
			called := make(chan struct{}, 1)
			management.EXPECT().GetFirmwarePolicy(gomock.Any(), "device", gomock.Any()).DoAndReturn(func(ctx context.Context, _ string, _ policy.Receipt, _ ...generatedclient.RequestEditorFn) (*policy.View, int, error) {
				called <- struct{}{}
				<-ctx.Done()
				return nil, 0, ctx.Err()
			}).Times(1)
			management.EXPECT().AcknowledgeFirmwarePolicy(gomock.Any(), "device", gomock.Any()).Return(204, nil).AnyTimes()
			m, err := receiver.New("device", dir, rw, management, log.NewPrefixLogger("activation"))
			require.NoError(t, err)
			run(t, m)
			if !retained {
				require.Never(t, func() bool { return len(called) > 0 }, 30*time.Millisecond, time.Millisecond)
				m.RequirePolicy()
				m.RequirePolicy()
			}
			select {
			case <-called:
			case <-time.After(time.Second):
				t.Fatal("required policy delivery did not start")
			}
		})
	}
}

func TestPolicyReceiveWithoutHTTP(t *testing.T) {
	one, _, _ := views(t)
	rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	dir := t.TempDir()
	ctrl := gomock.NewController(t)
	// Receive must not issue HTTP calls or depend on the poller's lifecycle.
	m, err := receiver.New("device", dir, rw, client.NewMockManagement(ctrl), log.NewPrefixLogger("receive"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	receipt, err := m.Receive(ctx, one)
	require.NoError(t, err)
	require.Equal(t, policy.Receipt{Revision: one.Revision, Digest: one.Digest}, receipt)
	data, err := rw.ReadFileLimited(filepath.Join(dir, "firmware/policy.json"), policy.MaxWireBytes)
	require.NoError(t, err)
	persisted, err := policy.ParseView(data)
	require.NoError(t, err)
	require.Equal(t, one, persisted)
	cancel()
	_, err = m.Receive(ctx, one)
	require.ErrorIs(t, err, context.Canceled)
}

func TestPolicyReceiveSerializesDurableAcceptance(t *testing.T) {
	one, two, _ := views(t)
	ctrl := gomock.NewController(t)
	rw := fileio.NewMockReadWriter(ctrl)
	rw.EXPECT().ReadFileLimited("/data/firmware/policy.json", int64(policy.MaxWireBytes)).Return(nil, os.ErrNotExist)
	writing := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	first := rw.EXPECT().WriteFileDurable("/data/firmware/policy.json", gomock.Any(), os.FileMode(0700), os.FileMode(0600)).DoAndReturn(func(string, []byte, os.FileMode, os.FileMode, ...fileio.FileOption) error {
		close(writing)
		<-release
		return nil
	})
	rw.EXPECT().WriteFileDurable("/data/firmware/policy.json", gomock.Any(), os.FileMode(0700), os.FileMode(0600)).Return(nil).After(first)
	m, err := receiver.New("device", "/data", rw, client.NewMockManagement(ctrl), log.NewPrefixLogger("serialized"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := m.Receive(ctx, one); firstDone <- err }()
	select {
	case <-writing:
	case <-ctx.Done():
		t.Fatal("first durable write did not start")
	}
	go func() { _, err := m.Receive(ctx, two); secondDone <- err }()
	require.Never(t, func() bool { return len(secondDone) > 0 }, 30*time.Millisecond, time.Millisecond)
	close(release)
	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondDone)
	current, err := m.Current()
	require.NoError(t, err)
	require.Equal(t, two, *current)
}

func TestPolicyGuardOrdersDurableResultAgainstRevocation(t *testing.T) {
	approved, revoked, binding := views(t)
	n := newNetwork(t)
	m := manager(t, t.TempDir(), fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter()), n)
	_, err := m.Receive(t.Context(), approved)
	require.NoError(t, err)

	started := make(chan policy.Receipt, 1)
	finish := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(finish) }) }
	defer release()
	accepted := make(chan error, 1)
	go func() {
		accepted <- m.WithApprovedRelease(t.Context(), binding.Name, binding.Version, binding.DescriptorDigest, func(receipt policy.Receipt) error {
			started <- receipt
			<-finish
			return nil
		})
	}()
	select {
	case receipt := <-started:
		require.Equal(t, approved.Revision, receipt.Revision)
	case <-time.After(time.Second):
		t.Fatal("approval callback did not start")
	}

	received := make(chan error, 1)
	go func() {
		_, err := m.Receive(t.Context(), revoked)
		received <- err
	}()
	require.Never(t, func() bool { return len(received) != 0 }, 25*time.Millisecond, time.Millisecond)
	waitCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	cancelled := make(chan error, 1)
	go func() {
		cancelled <- m.WithApprovedRelease(waitCtx, binding.Name, binding.Version, binding.DescriptorDigest, func(policy.Receipt) error {
			return errors.New("cancelled approval reached callback")
		})
	}()
	select {
	case err := <-cancelled:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("cancelled approval still waiting for policy gate")
	}

	release()
	for _, operation := range []struct {
		name string
		done <-chan error
	}{{"approval", accepted}, {"policy receive", received}} {
		select {
		case err := <-operation.done:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatalf("%s did not complete", operation.name)
		}
	}
	err = m.WithApprovedRelease(t.Context(), binding.Name, binding.Version, binding.DescriptorDigest, func(policy.Receipt) error {
		t.Fatal("revoked release reached result acceptance")
		return nil
	})
	require.Error(t, err)
}
