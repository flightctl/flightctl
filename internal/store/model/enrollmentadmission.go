package model

import (
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

type EnrollmentAdmission struct {
	Resource
	AdmissionID       uuid.UUID `gorm:"type:uuid;primaryKey;uniqueIndex"`
	Source            string
	Actor             string
	CertificateName   string
	PolicyName        string
	PolicyRevision    int64
	PolicyVersion     string
	EnrollmentRequest *JSONField[domain.EnrollmentRequest] `gorm:"type:jsonb"`
	Device            *JSONField[domain.Device]            `gorm:"type:jsonb"`
	Events            *JSONField[[]domain.Event]           `gorm:"type:jsonb"`
	Published         bool                                 `gorm:"index"`
}
