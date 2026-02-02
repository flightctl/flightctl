package transportv1alpha1

import (
	"encoding/json"
	"net/http"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/transport"
)

// (POST /api/v1/catalogs)
func (h *TransportHandler) CreateCatalog(w http.ResponseWriter, r *http.Request) {
	var catalog apiv1alpha1.Catalog
	if err := json.NewDecoder(r.Body).Decode(&catalog); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainCatalog := h.converter.Catalog().ToDomain(catalog)
	body, status := h.serviceHandler.CreateCatalog(r.Context(), transport.OrgIDFromContext(r.Context()), domainCatalog)
	apiResult := h.converter.Catalog().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (GET /api/v1/catalogs)
func (h *TransportHandler) ListCatalogs(w http.ResponseWriter, r *http.Request, params apiv1alpha1.ListCatalogsParams) {
	domainParams := h.converter.Catalog().ListParamsToDomain(params)
	body, status := h.serviceHandler.ListCatalogs(r.Context(), transport.OrgIDFromContext(r.Context()), domainParams)
	apiResult := h.converter.Catalog().ListFromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (GET /api/v1/catalogs/{name})
func (h *TransportHandler) GetCatalog(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetCatalog(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.Catalog().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/catalogs/{name})
func (h *TransportHandler) ReplaceCatalog(w http.ResponseWriter, r *http.Request, name string) {
	var catalog apiv1alpha1.Catalog
	if err := json.NewDecoder(r.Body).Decode(&catalog); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainCatalog := h.converter.Catalog().ToDomain(catalog)
	body, status := h.serviceHandler.ReplaceCatalog(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainCatalog)
	apiResult := h.converter.Catalog().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (DELETE /api/v1/catalogs/{name})
func (h *TransportHandler) DeleteCatalog(w http.ResponseWriter, r *http.Request, name string) {
	status := h.serviceHandler.DeleteCatalog(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	transport.SetResponse(w, nil, status)
}

// (PATCH /api/v1/catalogs/{name})
func (h *TransportHandler) PatchCatalog(w http.ResponseWriter, r *http.Request, name string) {
	var patch apiv1beta1.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainPatch := h.converter.Common().PatchRequestToDomain(patch)
	body, status := h.serviceHandler.PatchCatalog(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainPatch)
	apiResult := h.converter.Catalog().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (GET /api/v1/catalogs/{name}/status)
func (h *TransportHandler) GetCatalogStatus(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.serviceHandler.GetCatalogStatus(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.Catalog().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/catalogs/{name}/status)
func (h *TransportHandler) ReplaceCatalogStatus(w http.ResponseWriter, r *http.Request, name string) {
	var catalog apiv1alpha1.Catalog
	if err := json.NewDecoder(r.Body).Decode(&catalog); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainCatalog := h.converter.Catalog().ToDomain(catalog)
	body, status := h.serviceHandler.ReplaceCatalogStatus(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainCatalog)
	apiResult := h.converter.Catalog().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (PATCH /api/v1/catalogs/{name}/status)
func (h *TransportHandler) PatchCatalogStatus(w http.ResponseWriter, r *http.Request, name string) {
	var patch apiv1beta1.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainPatch := h.converter.Common().PatchRequestToDomain(patch)
	body, status := h.serviceHandler.PatchCatalogStatus(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainPatch)
	apiResult := h.converter.Catalog().FromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (GET /api/v1/catalogs/{name}/items)
func (h *TransportHandler) ListCatalogItems(w http.ResponseWriter, r *http.Request, name string, params apiv1alpha1.ListCatalogItemsParams) {
	domainParams := h.converter.Catalog().ListItemsParamsToDomain(params)
	body, status := h.serviceHandler.ListCatalogItems(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainParams)
	apiResult := h.converter.Catalog().ItemListFromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (POST /api/v1/catalogs/{name}/items)
func (h *TransportHandler) CreateCatalogItem(w http.ResponseWriter, r *http.Request, name string) {
	var item apiv1alpha1.CatalogItem
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainItem := h.converter.Catalog().ItemToDomain(item)
	body, status := h.serviceHandler.CreateCatalogItem(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainItem)
	apiResult := h.converter.Catalog().ItemFromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (GET /api/v1/catalogs/{name}/items/{item})
func (h *TransportHandler) GetCatalogItem(w http.ResponseWriter, r *http.Request, name string, item string) {
	body, status := h.serviceHandler.GetCatalogItem(r.Context(), transport.OrgIDFromContext(r.Context()), name, item)
	apiResult := h.converter.Catalog().ItemFromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/catalogs/{name}/items/{item})
func (h *TransportHandler) ReplaceCatalogItem(w http.ResponseWriter, r *http.Request, name string, itemName string) {
	var item apiv1alpha1.CatalogItem
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		transport.SetParseFailureResponse(w, err)
		return
	}

	domainItem := h.converter.Catalog().ItemToDomain(item)
	body, status := h.serviceHandler.ReplaceCatalogItem(r.Context(), transport.OrgIDFromContext(r.Context()), name, itemName, domainItem)
	apiResult := h.converter.Catalog().ItemFromDomain(body)
	transport.SetResponse(w, apiResult, status)
}

// (DELETE /api/v1/catalogs/{name}/items/{item})
func (h *TransportHandler) DeleteCatalogItem(w http.ResponseWriter, r *http.Request, name string, item string) {
	status := h.serviceHandler.DeleteCatalogItem(r.Context(), transport.OrgIDFromContext(r.Context()), name, item)
	transport.SetResponse(w, nil, status)
}
