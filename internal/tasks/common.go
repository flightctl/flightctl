package tasks

import "github.com/google/uuid"

type ResourceReference struct {
	OrgID uuid.UUID
	Kind  string
	Name  string
}

type TaskChannels map[string]chan ResourceReference

const (
	FleetTemplateRollout = "fleet-template-rollout"
	ResourceSync         = "resource-sync"

	ChannelSize  = 20
	ItemsPerPage = 1000
)

func MakeTaskChannels() TaskChannels {
	channels := make(map[string](chan ResourceReference))
	channels[FleetTemplateRollout] = make(chan ResourceReference, ChannelSize)
	channels[ResourceSync] = make(chan ResourceReference, ChannelSize)

	return channels
}

func (t TaskChannels) AllEmpty() bool {
	for c := range t {
		if len(t[c]) != 0 {
			return false
		}
	}
	return true
}

func (t TaskChannels) Close() {
	for c := range t {
		close(t[c])
	}
}

func (t TaskChannels) SubmitTask(taskName string, resource ResourceReference) {
	t[taskName] <- resource
}

func (t TaskChannels) GetTask(taskName string) ResourceReference {
	return <-t[taskName]
}
