package tasks

import api "github.com/flightctl/flightctl/api/v1alpha1"

func createTestEvent(kind api.ResourceKind, reason api.EventReason, name string) api.Event {
	return api.Event{
		InvolvedObject: api.ObjectReference{
			Kind: string(kind),
			Name: name,
		},
		Reason: reason,
	}
}

func createTestFleet(name string, rolloutPolicy *api.RolloutPolicy) *api.Fleet {
	fleetName := name
	generation := int64(1)

	return &api.Fleet{
		Metadata: api.ObjectMeta{
			Name:       &fleetName,
			Generation: &generation,
		},
		Spec: api.FleetSpec{
			RolloutPolicy: rolloutPolicy,
			Template: struct {
				Metadata *api.ObjectMeta `json:"metadata,omitempty"`
				Spec     api.DeviceSpec  `json:"spec"`
			}{
				Spec: api.DeviceSpec{
					Os: &api.DeviceOsSpec{
						Image: "test-image:latest",
					},
				},
			},
		},
	}
}
