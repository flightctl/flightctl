package periodic

import (
	"context"
	"errors"
	"testing"
	"time"

	enrollmentservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestEnrollmentAdmissionRecovery(t *testing.T) {
	for _, recoveryError := range []error{nil, errors.New("publication unavailable")} {
		t.Run("When recovering admissions it should call the provider service with bounded tenant scope", func(t *testing.T) {
			service := enrollmentservice.NewMockService(gomock.NewController(t))
			orgID := uuid.New()
			service.EXPECT().RecoverAdmissions(gomock.Any(), orgID).DoAndReturn(func(ctx context.Context, _ uuid.UUID) error {
				deadline, exists := ctx.Deadline()
				require.True(t, exists)
				require.WithinDuration(t, time.Now().Add(time.Minute), deadline, time.Second)
				return recoveryError
			})
			executor := &EnrollmentAdmissionExecutor{service: enrollmentservice.WrapWithTracing(service)}
			executor.Execute(context.Background(), logrus.New(), orgID)
		})
	}
}
