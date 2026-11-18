package periodic

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const PeriodicTaskTypeEnrollmentAdmission PeriodicTaskType = "enrollment-admission-publication"

type EnrollmentAdmissionExecutor struct {
	recover func(context.Context, uuid.UUID) error
}

func (executor *EnrollmentAdmissionExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgID uuid.UUID) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := executor.recover(ctx, orgID); err != nil {
		log.WithError(err).Error("Enrollment admission publication failed")
	}
}
