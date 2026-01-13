package service

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
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
