package spec

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"testing"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/policy"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestSavedConfigResources(t *testing.T) {
	for _, tc := range []struct{ name, payload string }{
		{"When resources contain explicit false it should retain them across saved specs", `[{"name":"registry:quay.io","type":"registry","registry":"quay.io","scheme":"https","skipServerVerification":false,"access":[{"scope":"","authentication":{"authType":"docker","username":"device","password":"saved-resource-sentinel"},"consumers":[{"type":"os"}]}]}]`},
		{"When resources are empty it should preserve an empty collection", `[]`},
		{"When legacy specs omit resources it should retain ordinary behavior", `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			rw := fileio.NewMockReadWriter(ctrl)
			queue := NewMockPriorityQueue(ctrl)
			watcher := NewMockWatcher(ctrl)
			policies := policy.NewMockManager(ctrl)
			logger := log.NewPrefixLogger("test")
			var logs bytes.Buffer
			logger.SetOutput(&logs)
			s := &manager{log: logger, deviceReadWriter: rw, queue: queue, watcher: watcher, policyManager: policies, cache: newCache(logger), currentPath: "current.json", desiredPath: "desired.json", rollbackPath: "rollback.json"}
			s.cache.current.renderedVersion = "0"
			s.cache.desired.renderedVersion = "0"
			desired := newVersionedDevice("2")
			desired.Spec.Os = &v1beta1.DeviceOsSpec{Image: "os-image"}
			if tc.payload != "null" {
				var resources []v1beta1.ConfigResource
				require.NoError(t, json.Unmarshal([]byte(tc.payload), &resources))
				desired.Spec.ConfigResources = &resources
			}
			files := map[string][]byte{}
			rw.EXPECT().WriteFile(gomock.Any(), gomock.Any(), fileio.DefaultFilePermissions).DoAndReturn(func(path string, data []byte, _ fs.FileMode, _ ...fileio.FileOption) error {
				files[path] = append([]byte(nil), data...)
				return nil
			}).AnyTimes()
			rw.EXPECT().ReadFile(gomock.Any()).DoAndReturn(func(path string) ([]byte, error) { return files[path], nil }).AnyTimes()
			gomock.InOrder(watcher.EXPECT().TryPop().Return(desired, true, nil), watcher.EXPECT().TryPop().Return(nil, false, nil))
			policies.EXPECT().Sync(gomock.Any(), desired.Spec).Return(nil)
			queue.EXPECT().Add(gomock.Any(), desired)
			queue.EXPECT().Next(gomock.Any()).Return(desired, true)
			queue.EXPECT().Remove(int64(2))
			ctx := context.Background()
			_, _, err := s.GetDesired(ctx)
			require.NoError(t, err)
			require.NoError(t, s.Upgrade(ctx))
			require.NoError(t, s.CreateRollback(ctx))
			// Read through a fresh manager to exercise restoration from saved JSON.
			restored := &manager{log: logger, cache: newCache(logger), deviceReadWriter: rw, currentPath: s.currentPath, desiredPath: s.desiredPath, rollbackPath: s.rollbackPath}
			for _, kind := range []Type{Current, Desired, Rollback} {
				device, err := restored.Read(kind)
				require.NoError(t, err)
				data, err := json.Marshal(device.Spec.ConfigResources)
				require.NoError(t, err)
				require.JSONEq(t, tc.payload, string(data))
				require.Equal(t, "2", device.Version())
			}
			require.NotContains(t, logs.String(), "saved-resource-sentinel")
		})
	}
}
