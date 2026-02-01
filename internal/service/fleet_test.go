package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func verifyFleetPatchFailed(require *require.Assertions, status domain.Status) {
	require.Equal(statusBadRequestCode, status.Code)
}

func testFleetPatch(require *require.Assertions, patch domain.PatchRequest) (*domain.Fleet, domain.Fleet, domain.Status) {
	fleet := domain.Fleet{
		ApiVersion: "v1",
		Kind:       "Fleet",
		Metadata: domain.ObjectMeta{
			Name:   lo.ToPtr("foo"),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: domain.FleetSpec{
			Selector: &domain.LabelSelector{
				MatchLabels: &map[string]string{"devKey": "devValue"},
			},
			Template: struct {
				Metadata *domain.ObjectMeta "json:\"metadata,omitempty\""
				Spec     domain.DeviceSpec  "json:\"spec\""
			}{
				Spec: domain.DeviceSpec{
					Os: &domain.DeviceOsSpec{
						Image: "img",
					},
				},
			},
		},
		Status: &domain.FleetStatus{
			Conditions: []domain.Condition{
				{
					Type:   "Approved",
					Status: "True",
				},
			},
		},
	}

	testStore := &TestStore{}
	wc := &DummyWorkerClient{}
	serviceHandler := &ServiceHandler{
		eventHandler: NewEventHandler(testStore, wc, log.InitLogs()),
		store:        testStore,
		workerClient: wc,
	}
	ctx := context.Background()
	testOrgId := uuid.New()
	orig, err := serviceHandler.store.Fleet().Create(ctx, testOrgId, &fleet, serviceHandler.callbackFleetUpdated)
	require.NoError(err)
	resp, status := serviceHandler.PatchFleet(ctx, testOrgId, "foo", patch)
	require.NotEqual(statusFailedCode, status.Code)
	_, err = serviceHandler.store.Event().List(ctx, testOrgId, store.ListParams{})
	require.NoError(err)
	return resp, *orig, status
}
func TestFleetPatchName(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := domain.PatchRequest{
		{Op: "replace", Path: "/metadata/name", Value: &value},
	}
	_, _, status := testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)
	pr = domain.PatchRequest{
		{Op: "remove", Path: "/metadata/name"},
	}
	_, _, status = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)
}

func TestFleetPatchKind(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := domain.PatchRequest{
		{Op: "replace", Path: "/kind", Value: &value},
	}
	_, _, status := testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)

	pr = domain.PatchRequest{
		{Op: "remove", Path: "/kind"},
	}
	_, _, status = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)
}

func TestFleetPatchAPIVersion(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := domain.PatchRequest{
		{Op: "replace", Path: "/apiVersion", Value: &value},
	}
	_, _, status := testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)

	pr = domain.PatchRequest{
		{Op: "remove", Path: "/apiVersion"},
	}
	_, _, status = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)
}

func TestFleetPatchSpec(t *testing.T) {
	require := require.New(t)
	var value interface{} = "newValue"
	pr := domain.PatchRequest{
		{Op: "replace", Path: "/spec/selector/matchLabels/devKey", Value: &value},
	}
	_, _, status := testResourceSyncPatch(require, pr)
	verifyFleetPatchFailed(require, status)

	value = 1234
	pr = domain.PatchRequest{
		{Op: "replace", Path: "/spec/selector/matchLabels/devKey", Value: &value},
	}
	_, _, status = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)

	value = "newimg"
	pr = domain.PatchRequest{
		{Op: "replace", Path: "/spec/template/spec/os/image", Value: &value},
	}
	resp, orig, status := testFleetPatch(require, pr)
	orig.Spec.Template.Spec.Os.Image = "newimg"
	require.Equal(statusSuccessCode, status.Code)
	require.Equal(orig, *resp)

	pr = domain.PatchRequest{
		{Op: "remove", Path: "/spec/template/spec/os"},
	}
	resp, orig, status = testFleetPatch(require, pr)
	orig.Spec.Template.Spec.Os = nil
	require.Equal(statusSuccessCode, status.Code)
	require.Equal(orig, *resp)

	value = "foo"
	pr = domain.PatchRequest{
		{Op: "replace", Path: "/spec/template/spec/os", Value: &value},
	}
	_, _, status = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)
}

func TestFleetPatchStatus(t *testing.T) {
	require := require.New(t)
	pr := domain.PatchRequest{
		{Op: "remove", Path: "/status/conditions/0"},
	}
	_, _, status := testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)
}

func TestFleetPatchNonExistingPath(t *testing.T) {
	require := require.New(t)
	var value interface{} = "foo"
	pr := domain.PatchRequest{
		{Op: "replace", Path: "/spec/doesnotexist", Value: &value},
	}
	_, _, status := testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)

	pr = domain.PatchRequest{
		{Op: "remove", Path: "/spec/doesnotexist"},
	}
	_, _, status = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)
}

func TestFleetPatchLabels(t *testing.T) {
	require := require.New(t)
	addLabels := map[string]string{"labelKey": "labelValue1"}
	var value interface{} = "labelValue1"
	pr := domain.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	resp, orig, status := testFleetPatch(require, pr)
	orig.Metadata.Labels = &addLabels
	require.Equal(statusSuccessCode, status.Code)
	require.Equal(orig, *resp)

	pr = domain.PatchRequest{
		{Op: "remove", Path: "/metadata/labels/labelKey"},
	}

	resp, orig, status = testFleetPatch(require, pr)
	orig.Metadata.Labels = &map[string]string{}
	require.Equal(statusSuccessCode, status.Code)
	require.Equal(orig, *resp)
}

func TestFleetNonExistingResource(t *testing.T) {
	require := require.New(t)
	var value interface{} = "labelValue1"
	pr := domain.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	testStore := &TestStore{}
	wc := &DummyWorkerClient{}
	serviceHandler := &ServiceHandler{
		eventHandler: NewEventHandler(testStore, wc, log.InitLogs()),
		store:        testStore,
		workerClient: wc,
	}
	ctx := context.Background()
	testOrgId := uuid.New()
	resp, status := serviceHandler.PatchFleet(ctx, testOrgId, "doesnotexist", pr)
	require.Equal(statusNotFoundCode, status.Code)
	require.Nil(resp)
}

func createTestFleet(name string, owner *string) domain.Fleet {
	return domain.Fleet{
		ApiVersion: "v1",
		Kind:       "Fleet",
		Metadata: domain.ObjectMeta{
			Name:   lo.ToPtr(name),
			Labels: &map[string]string{"labelKey": "labelValue"},
			Owner:  owner,
		},
		Spec: domain.FleetSpec{
			Selector: &domain.LabelSelector{
				MatchLabels: &map[string]string{"devKey": "devValue"},
			},
			Template: struct {
				Metadata *domain.ObjectMeta "json:\"metadata,omitempty\""
				Spec     domain.DeviceSpec  "json:\"spec\""
			}{
				Spec: domain.DeviceSpec{
					Os: &domain.DeviceOsSpec{
						Image: "img",
					},
				},
			},
		},
	}
}

func createDeleteTestServiceHandler() (*ServiceHandler, *TestStore) {
	testStore := &TestStore{}
	wc := &DummyWorkerClient{}
	serviceHandler := &ServiceHandler{
		eventHandler: NewEventHandler(testStore, wc, log.InitLogs()),
		store:        testStore,
		workerClient: wc,
	}
	return serviceHandler, testStore
}

func TestDeleteFleet(t *testing.T) {
	owner := "ResourceSync/my-resourcesync"

	tests := []struct {
		name                  string
		fleetName             string
		fleetOwner            *string
		createFleet           bool
		isResourceSyncRequest bool
		expectedStatusCode    int32
		expectedError         error
		expectFleetDeleted    bool
	}{
		{
			name:               "delete fleet without owner succeeds",
			fleetName:          "test-fleet",
			fleetOwner:         nil,
			createFleet:        true,
			expectedStatusCode: statusSuccessCode,
			expectFleetDeleted: true,
		},
		{
			name:               "delete non-existent fleet returns OK (idempotent)",
			fleetName:          "nonexistent-fleet",
			createFleet:        false,
			expectedStatusCode: statusSuccessCode,
			expectFleetDeleted: true,
		},
		{
			name:               "delete fleet with owner fails with conflict",
			fleetName:          "owned-fleet",
			fleetOwner:         &owner,
			createFleet:        true,
			expectedStatusCode: int32(http.StatusConflict),
			expectedError:      flterrors.ErrDeletingResourceWithOwnerNotAllowed,
			expectFleetDeleted: false,
		},
		{
			name:                  "resourceSync can delete fleets it owns",
			fleetName:             "resourcesync-owned-fleet",
			fleetOwner:            &owner,
			createFleet:           true,
			isResourceSyncRequest: true,
			expectedStatusCode:    statusSuccessCode,
			expectFleetDeleted:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			serviceHandler, _ := createDeleteTestServiceHandler()
			ctx := context.Background()
			testOrgId := uuid.New()

			if tt.createFleet {
				fleet := createTestFleet(tt.fleetName, tt.fleetOwner)
				_, err := serviceHandler.store.Fleet().Create(ctx, testOrgId, &fleet, serviceHandler.callbackFleetUpdated)
				require.NoError(err)
			}

			deleteCtx := ctx
			if tt.isResourceSyncRequest {
				deleteCtx = context.WithValue(ctx, consts.ResourceSyncRequestCtxKey, true)
			}

			status := serviceHandler.DeleteFleet(deleteCtx, testOrgId, tt.fleetName)
			require.Equal(tt.expectedStatusCode, status.Code)

			if tt.expectedError != nil {
				require.Equal(tt.expectedError.Error(), status.Message)
			}

			_, getStatus := serviceHandler.GetFleet(ctx, testOrgId, tt.fleetName, domain.GetFleetParams{})
			if tt.expectFleetDeleted {
				require.Equal(statusNotFoundCode, getStatus.Code)
			} else {
				require.Equal(statusSuccessCode, getStatus.Code)
			}
		})
	}
}
