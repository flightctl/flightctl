package registryconfig

import (
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

type Consumer string

const (
	ConsumerContainer Consumer = "container"
	ConsumerHelmChart Consumer = "helm-chart"
	ConsumerHelmImage Consumer = "helm-image"
	ConsumerOS        Consumer = "os"
)

type Candidate struct {
	OrganizationID uuid.UUID
	Repository     domain.Repository
}

type PullNeed struct {
	Reference string
	Consumer  Consumer
	User      string
	// Destination identifies the logical auth store shared by compatible consumers.
	Destination string
}

type Outcome string

const (
	OutcomeUnmatched Outcome = "unmatched"
	OutcomeSelected  Outcome = "selected"
	OutcomeAllowed   Outcome = "allowed"
	OutcomeBlocked   Outcome = "blocked"
	OutcomeAmbiguous Outcome = "ambiguous"
)

type Selection struct {
	Need     PullNeed
	Registry string
	Scope    string
	Outcome  Outcome
	Supplier *domain.Repository
}
