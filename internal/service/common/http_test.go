package common

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/stretchr/testify/require"
)

func TestNilOutManagedObjectMetaProperties(t *testing.T) {
	t.Run("When om is nil it should not panic", func(t *testing.T) {
		require.NotPanics(t, func() { NilOutManagedObjectMetaProperties(nil) })
	})

	t.Run("When om has managed properties set it should clear all of them", func(t *testing.T) {
		generation := int64(1)
		owner := "owner"
		annotations := map[string]string{"k": "v"}
		creationTimestamp := time.Now()
		deletionTimestamp := time.Now()
		om := &domain.ObjectMeta{
			Generation:        &generation,
			Owner:             &owner,
			Annotations:       &annotations,
			CreationTimestamp: &creationTimestamp,
			DeletionTimestamp: &deletionTimestamp,
		}

		NilOutManagedObjectMetaProperties(om)

		require.Nil(t, om.Generation)
		require.Nil(t, om.Owner)
		require.Nil(t, om.Annotations)
		require.Nil(t, om.CreationTimestamp)
		require.Nil(t, om.DeletionTimestamp)
	})
}

func TestStoreErrorToApiStatus(t *testing.T) {
	name := "foo"
	tests := []struct {
		name         string
		err          error
		created      bool
		kind         string
		expectedCode int32
	}{
		{
			name:         "When err is nil and created is true it should return StatusCreated",
			err:          nil,
			created:      true,
			expectedCode: 201,
		},
		{
			name:         "When err is nil and created is false it should return StatusOK",
			err:          nil,
			created:      false,
			expectedCode: 200,
		},
		{
			name:         "When err is ErrResourceNotFound it should return 404",
			err:          flterrors.ErrResourceNotFound,
			kind:         "Fleet",
			expectedCode: 404,
		},
		{
			name:         "When err is a bad-request error it should return 400",
			err:          flterrors.ErrResourceIsNil,
			expectedCode: 400,
		},
		{
			name:         "When err is a conflict error it should return 409",
			err:          flterrors.ErrDuplicateName,
			expectedCode: 409,
		},
		{
			name:         "When err is an unrecognized error it should return 500",
			err:          errors.New("some unmapped database error"),
			expectedCode: 500,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := StoreErrorToApiStatus(tt.err, tt.created, tt.kind, &name)
			require.Equal(t, tt.expectedCode, status.Code)
		})
	}
}

func TestApiStatusToErr(t *testing.T) {
	t.Run("When status code is 2xx it should return nil", func(t *testing.T) {
		require.NoError(t, ApiStatusToErr(domain.StatusOK()))
	})

	t.Run("When status code is non-2xx it should return an error with the status message", func(t *testing.T) {
		status := domain.StatusBadRequest("bad input")
		err := ApiStatusToErr(status)
		require.Error(t, err)
		require.Contains(t, err.Error(), "bad input")
	})
}

func TestHasConditionChanged(t *testing.T) {
	tests := []struct {
		name     string
		old      *domain.Condition
		new      *domain.Condition
		expected bool
	}{
		{
			name:     "When both conditions are nil it should return false",
			old:      nil,
			new:      nil,
			expected: false,
		},
		{
			name:     "When old is nil and new is non-nil it should return true",
			old:      nil,
			new:      &domain.Condition{Status: domain.ConditionStatusTrue},
			expected: true,
		},
		{
			name:     "When old is non-nil and new is nil it should return true",
			old:      &domain.Condition{Status: domain.ConditionStatusTrue},
			new:      nil,
			expected: true,
		},
		{
			name:     "When status differs it should return true",
			old:      &domain.Condition{Status: domain.ConditionStatusTrue},
			new:      &domain.Condition{Status: domain.ConditionStatusFalse},
			expected: true,
		},
		{
			name:     "When reason differs it should return true",
			old:      &domain.Condition{Status: domain.ConditionStatusTrue, Reason: "A"},
			new:      &domain.Condition{Status: domain.ConditionStatusTrue, Reason: "B"},
			expected: true,
		},
		{
			name:     "When message differs it should return true",
			old:      &domain.Condition{Status: domain.ConditionStatusTrue, Message: "A"},
			new:      &domain.Condition{Status: domain.ConditionStatusTrue, Message: "B"},
			expected: true,
		},
		{
			name:     "When conditions are identical it should return false",
			old:      &domain.Condition{Status: domain.ConditionStatusTrue, Reason: "A", Message: "M"},
			new:      &domain.Condition{Status: domain.ConditionStatusTrue, Reason: "A", Message: "M"},
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, HasConditionChanged(tt.old, tt.new))
		})
	}
}

func TestPrepareListParams(t *testing.T) {
	t.Run("When no limit is provided it should default to MaxRecordsPerListRequest", func(t *testing.T) {
		params, status := PrepareListParams(nil, nil, nil, nil)
		require.Equal(t, domain.StatusOK(), status)
		require.Equal(t, MaxRecordsPerListRequest, params.Limit)
	})

	t.Run("When limit exceeds MaxRecordsPerListRequest it should return a bad request status", func(t *testing.T) {
		limit := int32(MaxRecordsPerListRequest + 1)
		_, status := PrepareListParams(nil, nil, nil, &limit)
		require.Equal(t, int32(400), status.Code)
	})

	t.Run("When limit is negative it should return a bad request status", func(t *testing.T) {
		limit := int32(-1)
		_, status := PrepareListParams(nil, nil, nil, &limit)
		require.Equal(t, int32(400), status.Code)
	})

	t.Run("When the field selector is invalid it should return a bad request status", func(t *testing.T) {
		badSelector := "%%%invalid%%%"
		_, status := PrepareListParams(nil, nil, &badSelector, nil)
		require.Equal(t, int32(400), status.Code)
	})

	t.Run("When the label selector is invalid it should return a bad request status", func(t *testing.T) {
		badSelector := "%%%invalid%%%"
		_, status := PrepareListParams(nil, &badSelector, nil, nil)
		require.Equal(t, int32(400), status.Code)
	})
}

func TestApplyJSONPatch(t *testing.T) {
	t.Run("When the patch is valid it should apply the patch and validate against the schema", func(t *testing.T) {
		name := "foo"
		obj := &domain.ResourceSync{
			ApiVersion: "flightctl.io/v1beta1",
			Kind:       "ResourceSync",
			Metadata:   domain.ObjectMeta{Name: &name},
			Spec: domain.ResourceSyncSpec{
				Repository:     "repo",
				TargetRevision: "main",
				Path:           "/foo",
			},
		}
		var value interface{} = "bar"
		patch := domain.PatchRequest{
			{Op: "replace", Path: "/spec/repository", Value: &value},
		}
		newObj := &domain.ResourceSync{}
		err := ApplyJSONPatch(context.Background(), obj, newObj, patch, "/resourcesyncs/"+name)
		require.NoError(t, err)
		require.Equal(t, "bar", newObj.Spec.Repository)
	})

	t.Run("When the patch targets an unknown field it should return an error", func(t *testing.T) {
		name := "foo"
		obj := &domain.ResourceSync{
			ApiVersion: "flightctl.io/v1beta1",
			Kind:       "ResourceSync",
			Metadata:   domain.ObjectMeta{Name: &name},
			Spec: domain.ResourceSyncSpec{
				Repository:     "repo",
				TargetRevision: "main",
				Path:           "/foo",
			},
		}
		var value interface{} = "bar"
		patch := domain.PatchRequest{
			{Op: "replace", Path: "/spec/doesnotexist", Value: &value},
		}
		newObj := &domain.ResourceSync{}
		err := ApplyJSONPatch(context.Background(), obj, newObj, patch, "/resourcesyncs/"+name)
		require.Error(t, err)
	})
}
