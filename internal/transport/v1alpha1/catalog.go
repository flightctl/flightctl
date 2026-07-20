package transportv1alpha1

import (
	"encoding/json"
	"net/http"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	catalogservice "github.com/flightctl/flightctl/internal/service/catalog"
	"github.com/flightctl/flightctl/internal/transport"
)

// (POST /api/v1/catalogs)
func (h *TransportHandler) CreateCatalog(w http.ResponseWriter, r *http.Request) {
	var catalog apiv1alpha1.Catalog
	if err := json.NewDecoder(r.Body).Decode(&catalog); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainCatalog := h.converter.Catalog().ToDomain(catalog)
	body, status := catalogservice.CreateCatalogFromUntrusted(r.Context(), h.catalog, transport.OrgIDFromContext(r.Context()), domainCatalog)
	apiResult := h.converter.Catalog().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/catalogs)
func (h *TransportHandler) ListCatalogs(w http.ResponseWriter, r *http.Request, params apiv1alpha1.ListCatalogsParams) {
	domainParams := h.converter.Catalog().ListParamsToDomain(params)
	body, status := h.catalog.ListCatalogs(r.Context(), transport.OrgIDFromContext(r.Context()), domainParams)
	apiResult := h.converter.Catalog().ListFromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/catalogs/{name})
func (h *TransportHandler) GetCatalog(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.catalog.GetCatalog(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.Catalog().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/catalogs/{name})
func (h *TransportHandler) ReplaceCatalog(w http.ResponseWriter, r *http.Request, name string) {
	var catalog apiv1alpha1.Catalog
	if err := json.NewDecoder(r.Body).Decode(&catalog); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainCatalog := h.converter.Catalog().ToDomain(catalog)
	body, status := catalogservice.ReplaceCatalogFromUntrusted(r.Context(), h.catalog, transport.OrgIDFromContext(r.Context()), name, domainCatalog, true)
	apiResult := h.converter.Catalog().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (DELETE /api/v1/catalogs/{name})
func (h *TransportHandler) DeleteCatalog(w http.ResponseWriter, r *http.Request, name string) {
	status := h.catalog.DeleteCatalog(r.Context(), transport.OrgIDFromContext(r.Context()), name, true)
	h.SetResponse(w, nil, status)
}

// (PATCH /api/v1/catalogs/{name})
func (h *TransportHandler) PatchCatalog(w http.ResponseWriter, r *http.Request, name string) {
	var patch apiv1beta1.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainPatch := h.converter.Common().PatchRequestToDomain(patch)
	body, status := h.catalog.PatchCatalog(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainPatch, true)
	apiResult := h.converter.Catalog().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/catalogs/{name}/status)
func (h *TransportHandler) GetCatalogStatus(w http.ResponseWriter, r *http.Request, name string) {
	body, status := h.catalog.GetCatalogStatus(r.Context(), transport.OrgIDFromContext(r.Context()), name)
	apiResult := h.converter.Catalog().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/catalogs/{name}/status)
func (h *TransportHandler) ReplaceCatalogStatus(w http.ResponseWriter, r *http.Request, name string) {
	var catalog apiv1alpha1.Catalog
	if err := json.NewDecoder(r.Body).Decode(&catalog); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainCatalog := h.converter.Catalog().ToDomain(catalog)
	body, status := h.catalog.ReplaceCatalogStatus(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainCatalog)
	apiResult := h.converter.Catalog().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PATCH /api/v1/catalogs/{name}/status)
func (h *TransportHandler) PatchCatalogStatus(w http.ResponseWriter, r *http.Request, name string) {
	var patch apiv1beta1.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainPatch := h.converter.Common().PatchRequestToDomain(patch)
	body, status := h.catalog.PatchCatalogStatus(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainPatch)
	apiResult := h.converter.Catalog().FromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/catalogitems)
func (h *TransportHandler) ListAllCatalogItems(w http.ResponseWriter, r *http.Request, params apiv1alpha1.ListAllCatalogItemsParams) {
	domainParams := h.converter.Catalog().ListAllItemsParamsToDomain(params)
	body, status := h.catalog.ListAllCatalogItems(r.Context(), transport.OrgIDFromContext(r.Context()), domainParams)
	apiResult := h.converter.Catalog().ItemListFromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/catalogs/{name}/items)
func (h *TransportHandler) ListCatalogItems(w http.ResponseWriter, r *http.Request, name string, params apiv1alpha1.ListCatalogItemsParams) {
	domainParams := h.converter.Catalog().ListItemsParamsToDomain(params)
	body, status := h.catalog.ListCatalogItems(r.Context(), transport.OrgIDFromContext(r.Context()), name, domainParams)
	apiResult := h.converter.Catalog().ItemListFromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (POST /api/v1/catalogs/{name}/items)
func (h *TransportHandler) CreateCatalogItem(w http.ResponseWriter, r *http.Request, name string) {
	var item apiv1alpha1.CatalogItem
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainItem := h.converter.Catalog().ItemToDomain(item)
	body, status := catalogservice.CreateCatalogItemFromUntrusted(r.Context(), h.catalog, transport.OrgIDFromContext(r.Context()), name, domainItem)
	apiResult := h.converter.Catalog().ItemFromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (GET /api/v1/catalogs/{name}/items/{item})
func (h *TransportHandler) GetCatalogItem(w http.ResponseWriter, r *http.Request, name string, item string) {
	body, status := h.catalog.GetCatalogItem(r.Context(), transport.OrgIDFromContext(r.Context()), name, item)
	apiResult := h.converter.Catalog().ItemFromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PUT /api/v1/catalogs/{name}/items/{item})
func (h *TransportHandler) ReplaceCatalogItem(w http.ResponseWriter, r *http.Request, name string, itemName string) {
	var item apiv1alpha1.CatalogItem
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainItem := h.converter.Catalog().ItemToDomain(item)
	body, status := catalogservice.ReplaceCatalogItemFromUntrusted(r.Context(), h.catalog, transport.OrgIDFromContext(r.Context()), name, itemName, domainItem, true)
	apiResult := h.converter.Catalog().ItemFromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (PATCH /api/v1/catalogs/{name}/items/{item})
func (h *TransportHandler) PatchCatalogItem(w http.ResponseWriter, r *http.Request, name string, itemName string) {
	var patch apiv1beta1.PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		h.SetParseFailureResponse(w, err)
		return
	}

	domainPatch := h.converter.Common().PatchRequestToDomain(patch)
	body, status := h.catalog.PatchCatalogItem(r.Context(), transport.OrgIDFromContext(r.Context()), name, itemName, domainPatch, true)
	apiResult := h.converter.Catalog().ItemFromDomain(body)
	h.SetResponse(w, apiResult, status)
}

// (DELETE /api/v1/catalogs/{name}/items/{item})
func (h *TransportHandler) DeleteCatalogItem(w http.ResponseWriter, r *http.Request, name string, item string) {
	status := h.catalog.DeleteCatalogItem(r.Context(), transport.OrgIDFromContext(r.Context()), name, item, true)
	h.SetResponse(w, nil, status)
}
