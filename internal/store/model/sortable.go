package model

import (
	"github.com/flightctl/flightctl/internal/store/selector"
)

type Sortable interface {
	SortableSelectors() selector.SelectorNameSet
	ValueOf(selector selector.SelectorName) any
}

func (m *Device) SortableSelectors() selector.SelectorNameSet {
	return selector.NewSelectorFieldNameSet().Add(
		"metadata.name",
		"metadata.alias",
		"metadata.owner",
		"status.summary.status",
		"status.applicationsSummary.status",
		"status.updated.status",
		"status.lastSeen",
	)
}

func (m *Device) ValueOf(name selector.SelectorName) any {
	switch name {
	case "metadata.name":
		return m.Name
	case "metadata.alias":
		return m.Alias
	case "metadata.owner":
		return m.Owner
	}

	api := m.ToApiResource()
	if api.Status != nil {
		switch name {
		case "status.summary.status":
			return string(api.Status.Summary.Status)
		case "status.applicationsSummary.status":
			return string(api.Status.ApplicationsSummary.Status)
		case "status.updated.status":
			return string(api.Status.Updated.Status)
		case "status.lastSeen":
			return api.Status.LastSeen
		}
	}
	return nil
}

func (m *Fleet) SortableSelectors() selector.SelectorNameSet {
	return selector.NewSelectorFieldNameSet().Add(
		"metadata.name",
		"spec.template.spec.os.image",
	)
}

func (m *Fleet) ValueOf(name selector.SelectorName) any {
	switch name {
	case "metadata.name":
		return m.Name
	}

	api := m.ToApiResource()
	switch name {
	case "spec.template.spec.os.image":
		if api.Spec.Template.Spec.Os != nil {
			return string(api.Spec.Template.Spec.Os.Image)
		}
	}
	return nil
}

func (m *EnrollmentRequest) SortableSelectors() selector.SelectorNameSet {
	return selector.NewSelectorFieldNameSet().Add(
		"metadata.name",
		"metadata.creationTimestamp",
	)
}

func (m *EnrollmentRequest) ValueOf(name selector.SelectorName) any {
	switch name {
	case "metadata.name":
		return m.Name
	case "metadata.creationTimestamp":
		return m.CreatedAt
	default:
		return nil
	}
}

func (m *Repository) SortableSelectors() selector.SelectorNameSet {
	return selector.NewSelectorFieldNameSet().Add(
		"metadata.name",
		"spec.type",
		"spec.url",
	)
}

func (m *Repository) ValueOf(name selector.SelectorName) any {
	switch name {
	case "metadata.name":
		return m.Name
	}

	api, _ := m.ToApiResource()
	switch name {
	case "spec.type":
		if spec, err := api.Spec.GetGenericRepoSpec(); err != nil {
			return string(spec.Type)
		}
	case "spec.url":
		if spec, err := api.Spec.GetGenericRepoSpec(); err != nil {
			return string(spec.Type)
		}
	}
	return nil
}
