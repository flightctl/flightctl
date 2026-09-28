package transportv1beta1

import (
	"encoding/json"
	"net/http"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	labelsyncmappingservice "github.com/flightctl/flightctl/internal/service/labelsyncmapping"
	"github.com/flightctl/flightctl/internal/transport"
)

func (h *TransportHandler) CreateLabelSyncMapping(w http.ResponseWriter, r *http.Request) {
	var mapping apiv1beta1.LabelSyncMapping
	if err := json.NewDecoder(r.Body).Decode(&mapping); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}
	body, status := labelsyncmappingservice.CreateLabelSyncMappingFromUntrusted(r.Context(), h.labelsyncmapping, transport.OrgIDFromContext(r.Context()), h.converter.LabelSyncMapping().ToDomain(mapping))
	h.SetResponse(w, h.converter.LabelSyncMapping().FromDomain(body), status)
}

func (h *TransportHandler) ListLabelSyncMappings(w http.ResponseWriter, r *http.Request, params apiv1beta1.ListLabelSyncMappingsParams) {
	body, status := h.labelsyncmapping.ListLabelSyncMappings(r.Context(), transport.OrgIDFromContext(r.Context()), h.converter.LabelSyncMapping().ListParamsToDomain(params))
	h.SetResponse(w, h.converter.LabelSyncMapping().ListFromDomain(body), status)
}

func (h *TransportHandler) GetLabelSyncMapping(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.labelsyncmapping.GetLabelSyncMapping(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	h.SetResponse(w, h.converter.LabelSyncMapping().FromDomain(body), status)
}

func (h *TransportHandler) ReplaceLabelSyncMapping(w http.ResponseWriter, r *http.Request, name string) {
	var mapping apiv1beta1.LabelSyncMapping
	if err := json.NewDecoder(r.Body).Decode(&mapping); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}
	body, status := labelsyncmappingservice.ReplaceLabelSyncMappingFromUntrusted(r.Context(), h.labelsyncmapping, transport.OrgIDFromContext(r.Context()), name, h.converter.LabelSyncMapping().ToDomain(mapping))
	h.SetResponse(w, h.converter.LabelSyncMapping().FromDomain(body), status)
}

func (h *TransportHandler) DeleteLabelSyncMapping(w http.ResponseWriter, r *http.Request, name string) {
	status := h.labelsyncmapping.DeleteLabelSyncMapping(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	h.SetResponse(w, nil, status)
}

func (h *TransportHandler) PatchLabelSyncMapping(w http.ResponseWriter, r *http.Request, name string) {
	var patch apiv1beta1.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}
	body, status := h.labelsyncmapping.PatchLabelSyncMapping(r.Context(), transport.OrgIDFromContext(r.Context()), name, h.converter.Common().PatchRequestToDomain(patch))
	h.SetResponse(w, h.converter.LabelSyncMapping().FromDomain(body), status)
}
