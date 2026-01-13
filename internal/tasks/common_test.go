package tasks

import "github.com/flightctl/flightctl/internal/domain"

func createTestEvent(kind domain.ResourceKind, reason domain.EventReason, name string) domain.Event {
	return domain.Event{
		InvolvedObject: domain.ObjectReference{
			Kind: string(kind),
			Name: name,
		},
		Reason: reason,
	}
}

func createTestFleet(name string, rolloutPolicy *domain.RolloutPolicy) *domain.Fleet {
	fleetName := name
	generation := int64(1)

	return &domain.Fleet{
		Metadata: domain.ObjectMeta{
			Name:       &fleetName,
			Generation: &generation,
		},
		Spec: domain.FleetSpec{
			RolloutPolicy: rolloutPolicy,
			Template: struct {
				Metadata *domain.ObjectMeta `json:"metadata,omitempty"`
				Spec     domain.DeviceSpec  `json:"spec"`
			}{
				Spec: domain.DeviceSpec{
					Os: &domain.DeviceOsSpec{
						Image: "test-image:latest",
					},
				},
			},
		},
	}
}
