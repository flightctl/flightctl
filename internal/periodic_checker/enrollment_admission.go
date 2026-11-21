package periodic

import (
	"context"
	"time"

	enrollmentservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const PeriodicTaskTypeEnrollmentAdmission PeriodicTaskType = "enrollment-admission-publication"

type EnrollmentAdmissionExecutor struct {
	service enrollmentservice.Service
}

func (executor *EnrollmentAdmissionExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgID uuid.UUID) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := executor.service.RecoverAdmissions(ctx, orgID); err != nil {
		log.WithError(err).Error("Enrollment admission publication failed")
	}
}
