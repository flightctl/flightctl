package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"reflect"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/sosreport"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/go-openapi/swag"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// (POST /api/v1/devices)
func (h *ServiceHandler) CreateDevice(ctx context.Context, request server.CreateDeviceRequestObject) (server.CreateDeviceResponseObject, error) {
	if request.Body.Spec != nil && request.Body.Spec.Decommissioning != nil {
		h.log.WithError(flterrors.ErrDecommission).Error("attempt to create decommissioned device")
		return server.CreateDevice400JSONResponse(api.StatusBadRequest(flterrors.ErrDecommission.Error())), nil
	}

	orgId := store.NullOrgId

	// don't set fields that are managed by the service
	request.Body.Status = nil
	common.NilOutManagedObjectMetaProperties(&request.Body.Metadata)

	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.CreateDevice400JSONResponse(api.StatusBadRequest(errors.Join(errs...).Error())), nil
	}

	common.UpdateServiceSideStatus(ctx, h.store, h.log, orgId, request.Body)

	result, err := h.store.Device().Create(ctx, orgId, request.Body, h.callbackManager.DeviceUpdatedCallback)
	switch {
	case err == nil:
		return server.CreateDevice201JSONResponse(*result), nil
	case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrIllegalResourceVersionFormat):
		return server.CreateDevice400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrDuplicateName):
		return server.CreateDevice409JSONResponse(api.StatusResourceVersionConflict(err.Error())), nil
	default:
		return nil, err
	}
}

// (GET /api/v1/devices)
func (h *ServiceHandler) ListDevices(ctx context.Context, request server.ListDevicesRequestObject) (server.ListDevicesResponseObject, error) {
	orgId := store.NullOrgId

	var (
		fieldSelector *selector.FieldSelector
		err           error
	)
	if request.Params.FieldSelector != nil {
		if fieldSelector, err = selector.NewFieldSelector(*request.Params.FieldSelector); err != nil {
			return server.ListDevices400JSONResponse(api.StatusBadRequest(fmt.Sprintf("failed to parse field selector: %v", err))), nil
		}
	}

	var labelSelector *selector.LabelSelector
	if request.Params.LabelSelector != nil {
		if labelSelector, err = selector.NewLabelSelector(*request.Params.LabelSelector); err != nil {
			return server.ListDevices400JSONResponse(api.StatusBadRequest(fmt.Sprintf("failed to parse label selector: %v", err))), nil
		}
	}

	// Check if SummaryOnly is true
	if request.Params.SummaryOnly != nil && *request.Params.SummaryOnly {
		// Check for unsupported parameters
		if request.Params.Limit != nil || request.Params.Continue != nil {
			return server.ListDevices400JSONResponse(api.StatusBadRequest("parameters such as 'limit', and 'continue' are not supported when 'summaryOnly' is true")), nil
		}

		result, err := h.store.Device().Summary(ctx, orgId, store.ListParams{
			FieldSelector: fieldSelector,
			LabelSelector: labelSelector,
		})

		switch err {
		case nil:
			// Create an empty DeviceList and set the summary
			emptyList, _ := model.DevicesToApiResource([]model.Device{}, nil, nil)
			emptyList.Summary = result
			return server.ListDevices200JSONResponse(emptyList), nil
		default:
			return nil, err
		}
	}

	cont, err := store.ParseContinueString(request.Params.Continue)
	if err != nil {
		return server.ListDevices400JSONResponse(api.StatusBadRequest(fmt.Sprintf("failed to parse continue parameter: %v", err))), nil
	}

	listParams := store.ListParams{
		Limit:         int(swag.Int32Value(request.Params.Limit)),
		Continue:      cont,
		FieldSelector: fieldSelector,
		LabelSelector: labelSelector,
	}
	if listParams.Limit == 0 {
		listParams.Limit = store.MaxRecordsPerListRequest
	}
	if listParams.Limit > store.MaxRecordsPerListRequest {
		return server.ListDevices400JSONResponse(api.StatusBadRequest(fmt.Sprintf("limit cannot exceed %d", store.MaxRecordsPerListRequest))), nil
	}

	result, err := h.store.Device().List(ctx, orgId, listParams)
	if err == nil {
		return server.ListDevices200JSONResponse(*result), nil
	}

	var se *selector.SelectorError

	switch {
	case errors.Is(err, flterrors.ErrLimitParamOutOfBounds):
		return server.ListDevices400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case selector.AsSelectorError(err, &se):
		return server.ListDevices400JSONResponse(api.StatusBadRequest(se.Error())), nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/devices)
func (h *ServiceHandler) DeleteDevices(ctx context.Context, request server.DeleteDevicesRequestObject) (server.DeleteDevicesResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.Device().DeleteAll(ctx, orgId, h.callbackManager.AllDevicesDeletedCallback)
	switch err {
	case nil:
		return server.DeleteDevices200JSONResponse(api.StatusOK()), nil
	default:
		return nil, err
	}
}

// (GET /api/v1/devices/{name})
func (h *ServiceHandler) ReadDevice(ctx context.Context, request server.ReadDeviceRequestObject) (server.ReadDeviceResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.Device().Get(ctx, orgId, request.Name)
	switch {
	case err == nil:
		return server.ReadDevice200JSONResponse(*result), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.ReadDevice404JSONResponse(api.StatusResourceNotFound("Device", request.Name)), nil
	default:
		return nil, err
	}
}

func DeviceVerificationCallback(before, after *api.Device) error {
	// Ensure the device wasn't decommissioned
	if before != nil && before.Spec != nil && before.Spec.Decommissioning != nil {
		return flterrors.ErrDecommission
	}
	return nil
}

// (PUT /api/v1/devices/{name})
func (h *ServiceHandler) ReplaceDevice(ctx context.Context, request server.ReplaceDeviceRequestObject) (server.ReplaceDeviceResponseObject, error) {
	if request.Body.Spec != nil && request.Body.Spec.Decommissioning != nil {
		h.log.WithError(flterrors.ErrDecommission).Error("attempt to set decommissioned status when replacing device, or to replace decommissioned device")
		return server.ReplaceDevice400JSONResponse(api.StatusBadRequest(flterrors.ErrDecommission.Error())), nil
	}

	orgId := store.NullOrgId

	// don't overwrite fields that are managed by the service
	request.Body.Status = nil
	common.NilOutManagedObjectMetaProperties(&request.Body.Metadata)

	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.ReplaceDevice400JSONResponse(api.StatusBadRequest(errors.Join(errs...).Error())), nil
	}
	if request.Name != *request.Body.Metadata.Name {
		return server.ReplaceDevice400JSONResponse(api.StatusBadRequest("resource name specified in metadata does not match name in path")), nil
	}

	common.UpdateServiceSideStatus(ctx, h.store, h.log, orgId, request.Body)

	result, created, err := h.store.Device().CreateOrUpdate(ctx, orgId, request.Body, nil, true, DeviceVerificationCallback, h.callbackManager.DeviceUpdatedCallback)
	switch {
	case err == nil:
		if created {
			return server.ReplaceDevice201JSONResponse(*result), nil
		} else {
			return server.ReplaceDevice200JSONResponse(*result), nil
		}
	case errors.Is(err, flterrors.ErrResourceIsNil):
		return server.ReplaceDevice400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrResourceNameIsNil), errors.Is(err, flterrors.ErrIllegalResourceVersionFormat):
		return server.ReplaceDevice400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.ReplaceDevice404JSONResponse(api.StatusResourceNotFound("Device", request.Name)), nil
	case errors.Is(err, flterrors.ErrUpdatingResourceWithOwnerNotAllowed), errors.Is(err, flterrors.ErrNoRowsUpdated), errors.Is(err, flterrors.ErrResourceVersionConflict):
		return server.ReplaceDevice409JSONResponse(api.StatusResourceVersionConflict(err.Error())), nil
	default:
		return nil, err
	}
}

// (DELETE /api/v1/devices/{name})
func (h *ServiceHandler) DeleteDevice(ctx context.Context, request server.DeleteDeviceRequestObject) (server.DeleteDeviceResponseObject, error) {
	orgId := store.NullOrgId

	err := h.store.Device().Delete(ctx, orgId, request.Name, h.callbackManager.DeviceUpdatedCallback)
	switch {
	case err == nil:
		return server.DeleteDevice200JSONResponse{}, nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.DeleteDevice404JSONResponse(api.StatusResourceNotFound("Device", request.Name)), nil
	default:
		return nil, err
	}
}

// (GET /api/v1/devices/{name}/status)
func (h *ServiceHandler) ReadDeviceStatus(ctx context.Context, request server.ReadDeviceStatusRequestObject) (server.ReadDeviceStatusResponseObject, error) {
	orgId := store.NullOrgId

	result, err := h.store.Device().Get(ctx, orgId, request.Name)
	switch {
	case err == nil:
		return server.ReadDeviceStatus200JSONResponse(*result), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.ReadDeviceStatus404JSONResponse(api.StatusResourceNotFound("Device", request.Name)), nil
	default:
		return nil, err
	}
}

// (PUT /api/v1/devices/{name}/status)
func (h *ServiceHandler) ReplaceDeviceStatus(ctx context.Context, request server.ReplaceDeviceStatusRequestObject) (server.ReplaceDeviceStatusResponseObject, error) {
	return common.ReplaceDeviceStatus(ctx, h.store, h.log, request)
}

// (GET /api/v1/devices/{name}/rendered)
func (h *ServiceHandler) GetRenderedDevice(ctx context.Context, request server.GetRenderedDeviceRequestObject) (server.GetRenderedDeviceResponseObject, error) {
	return common.GetRenderedDevice(ctx, h.store, h.log, request, h.agentEndpoint)
}

// (PATCH /api/v1/devices/{name})
// Only metadata.labels and spec can be patched. If we try to patch other fields, HTTP 400 Bad Request is returned.
func (h *ServiceHandler) PatchDevice(ctx context.Context, request server.PatchDeviceRequestObject) (server.PatchDeviceResponseObject, error) {
	orgId := store.NullOrgId

	currentObj, err := h.store.Device().Get(ctx, orgId, request.Name)
	if err != nil {
		switch {
		case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrResourceNameIsNil):
			return server.PatchDevice400JSONResponse(api.StatusBadRequest(err.Error())), nil
		case errors.Is(err, flterrors.ErrResourceNotFound):
			return server.PatchDevice404JSONResponse(api.StatusResourceNotFound("Device", request.Name)), nil
		default:
			return nil, err
		}
	}

	newObj := &api.Device{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, *request.Body, "/api/v1/devices/"+request.Name)
	if err != nil {
		return server.PatchDevice400JSONResponse(api.StatusBadRequest(err.Error())), nil
	}

	if errs := newObj.Validate(); len(errs) > 0 {
		return server.PatchDevice400JSONResponse(api.StatusBadRequest(errors.Join(errs...).Error())), nil
	}
	if newObj.Metadata.Name == nil || *currentObj.Metadata.Name != *newObj.Metadata.Name {
		return server.PatchDevice400JSONResponse(api.StatusBadRequest("metadata.name is immutable")), nil
	}
	if currentObj.ApiVersion != newObj.ApiVersion {
		return server.PatchDevice400JSONResponse(api.StatusBadRequest("apiVersion is immutable")), nil
	}
	if currentObj.Kind != newObj.Kind {
		return server.PatchDevice400JSONResponse(api.StatusBadRequest("kind is immutable")), nil
	}
	if !reflect.DeepEqual(currentObj.Status, newObj.Status) {
		return server.PatchDevice400JSONResponse(api.StatusBadRequest("status is immutable")), nil
	}
	if newObj.Spec != nil && newObj.Spec.Decommissioning != nil {
		return server.PatchDevice400JSONResponse(api.StatusBadRequest("spec.decommissioning cannot be changed via patch request")), nil
	}

	common.NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	var updateCallback func(uuid.UUID, *api.Device, *api.Device)

	if h.callbackManager != nil {
		updateCallback = h.callbackManager.DeviceUpdatedCallback
	}

	common.UpdateServiceSideStatus(ctx, h.store, h.log, orgId, newObj)

	// create
	result, err := h.store.Device().Update(ctx, orgId, newObj, nil, true, DeviceVerificationCallback, updateCallback)

	switch {
	case err == nil:
		return server.PatchDevice200JSONResponse(*result), nil
	case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrResourceNameIsNil), errors.Is(err, flterrors.ErrIllegalResourceVersionFormat):
		return server.PatchDevice400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.PatchDevice404JSONResponse(api.StatusResourceNotFound("Device", request.Name)), nil
	case errors.Is(err, flterrors.ErrNoRowsUpdated), errors.Is(err, flterrors.ErrResourceVersionConflict), errors.Is(err, flterrors.ErrUpdatingResourceWithOwnerNotAllowed):
		return server.PatchDevice409JSONResponse(api.StatusConflict(err.Error())), nil
	default:
		return nil, err
	}
}

// (PATCH /api/v1/devices/{name}/status)
func (h *ServiceHandler) PatchDeviceStatus(ctx context.Context, request server.PatchDeviceStatusRequestObject) (server.PatchDeviceStatusResponseObject, error) {
	return nil, fmt.Errorf("not yet implemented")
}

// (PUT /api/v1/devices/{name}/decommission)
func (h *ServiceHandler) DecommissionDevice(ctx context.Context, request server.DecommissionDeviceRequestObject) (server.DecommissionDeviceResponseObject, error) {
	orgId := store.NullOrgId

	deviceObj, err := h.store.Device().Get(ctx, orgId, request.Name)
	if err != nil {
		switch {
		case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrResourceNameIsNil):
			return server.DecommissionDevice400JSONResponse(api.StatusBadRequest(err.Error())), nil
		case errors.Is(err, flterrors.ErrResourceNotFound):
			return server.DecommissionDevice404JSONResponse(api.StatusResourceNotFound("Device", request.Name)), nil
		default:
			return nil, err
		}
	}
	if deviceObj.Spec != nil && deviceObj.Spec.Decommissioning != nil {
		return nil, fmt.Errorf("device already has decommissioning requested")
	}

	deviceObj.Status.Lifecycle.Status = api.DeviceLifecycleStatusDecommissioning
	deviceObj.Spec.Decommissioning = request.Body

	// these fields must be un-set so that device is no longer associated with any fleet
	deviceObj.Metadata.Owner = nil
	deviceObj.Metadata.Labels = nil

	var updateCallback func(uuid.UUID, *api.Device, *api.Device)

	if h.callbackManager != nil {
		updateCallback = h.callbackManager.DeviceUpdatedCallback
	}

	// set the fromAPI bool to 'false', otherwise updating the spec.decommissionRequested of a device is blocked
	result, err := h.store.Device().Update(ctx, orgId, deviceObj, []string{"status", "owner"}, false, DeviceVerificationCallback, updateCallback)

	switch {
	case err == nil:
		return server.DecommissionDevice200JSONResponse(*result), nil
	case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrResourceNameIsNil), errors.Is(err, flterrors.ErrIllegalResourceVersionFormat):
		return server.DecommissionDevice400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.DecommissionDevice404JSONResponse(api.StatusResourceNotFound("Device", request.Name)), nil
	default:
		return nil, err
	}
}

type sosReportGetter struct {
	store   store.Store
	log     logrus.FieldLogger
	request server.GetSosReportRequestObject
	rcvChan chan *multipart.Reader
	errChan chan error
}

func (h *ServiceHandler) sosReportGetter(request server.GetSosReportRequestObject) *sosReportGetter {
	return &sosReportGetter{
		store:   h.store,
		log:     h.log,
		request: request,
		rcvChan: make(chan *multipart.Reader),
		errChan: make(chan error),
	}
}

func (s *sosReportGetter) updateAnnotation(ctx context.Context, orgId uuid.UUID, updater func([]string) []string) (server.GetSosReportResponseObject, error) {
	device, err := s.store.Device().Get(ctx, orgId, s.request.Name)
	if err != nil {
		switch {
		case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrResourceNameIsNil):
			return server.GetSosReport400JSONResponse(api.StatusBadRequest(err.Error())), nil
		case errors.Is(err, flterrors.ErrResourceNotFound):
			return server.GetSosReport404JSONResponse(api.StatusResourceNotFound("Device", s.request.Name)), nil
		default:
			return nil, err
		}
	}
	var ids []string
	annotation, exists := util.GetFromMap(lo.FromPtr(device.Metadata.Annotations), api.DeviceAnnotationSosReports)
	if exists {
		if err := json.Unmarshal([]byte(annotation), &ids); err != nil {
			return server.GetSosReport500JSONResponse(api.StatusInternalServerError(err.Error())), nil
		}
	}
	ids = updater(ids)
	b, err := json.Marshal(&ids)
	if err != nil {
		return server.GetSosReport500JSONResponse(api.StatusInternalServerError(err.Error())), nil
	}
	annotations := map[string]string{
		api.DeviceAnnotationSosReports: string(b),
	}
	err = s.store.Device().UpdateAnnotations(ctx, orgId, s.request.Name, annotations, nil)
	if err != nil {
		return server.GetSosReport500JSONResponse(api.StatusInternalServerError(err.Error())), nil
	}
	return nil, nil
}

func (s *sosReportGetter) addSession(ctx context.Context, orgId, id uuid.UUID) (server.GetSosReportResponseObject, error) {
	sosreport.Sessions.Add(id, s.rcvChan, s.errChan)
	return s.updateAnnotation(ctx, orgId, func(arr []string) []string { return append(arr, id.String()) })
}

func (s *sosReportGetter) removeSession(ctx context.Context, orgId, id uuid.UUID) (server.GetSosReportResponseObject, error) {
	sosreport.Sessions.Remove(id)
	return s.updateAnnotation(ctx, orgId, func(arr []string) []string { return lo.Without(arr, id.String()) })
}

type getSosReport200ApplicationoctetStreamResponse struct {
	srcPart *multipart.Part
	s       *sosReportGetter
}

func (g getSosReport200ApplicationoctetStreamResponse) VisitGetSosReportResponse(w http.ResponseWriter) (err error) {
	defer func() {
		g.s.errChan <- err
		close(g.s.errChan)
	}()
	err = server.GetSosReport200ApplicationoctetStreamResponse{
		Body: g.srcPart,
		Headers: server.GetSosReport200ResponseHeaders{
			ContentDisposition: fmt.Sprintf(`attachment; filename="%s"`, g.srcPart.FileName()),
		},
	}.VisitGetSosReportResponse(w)
	return
}

func (s *sosReportGetter) emitReport(srcPart *multipart.Part) server.GetSosReportResponseObject {
	return getSosReport200ApplicationoctetStreamResponse{
		srcPart: srcPart,
		s:       s,
	}
}

type getSosReportErrorByStatus api.Status

func (response getSosReportErrorByStatus) VisitGetSosReportResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(int(response.Code))
	return json.NewEncoder(w).Encode(response)
}

func (s *sosReportGetter) emitError(srcPart *multipart.Part) server.GetSosReportResponseObject {
	defer close(s.errChan)
	var status api.Status
	d := json.NewDecoder(srcPart)
	if err := d.Decode(&status); err != nil {
		err = fmt.Errorf("failed to decode error status: %w", err)
		s.errChan <- err
		return server.GetSosReport500JSONResponse(api.StatusInternalServerError(fmt.Sprintf("failed to decode error status: %v", err)))
	}
	return getSosReportErrorByStatus(status)
}

func (s *sosReportGetter) emitUnexpected(srcPart *multipart.Part) server.GetSosReportResponseObject {
	defer close(s.errChan)
	err := fmt.Errorf("unexpected part form name %s", srcPart.FormName())
	s.errChan <- err
	return server.GetSosReport500JSONResponse(api.StatusInternalServerError(err.Error()))
}

func (s *sosReportGetter) getSosReportObject(reader *multipart.Reader) server.GetSosReportResponseObject {
	part, err := reader.NextPart()
	if err != nil {
		s.errChan <- err
		close(s.errChan)
		return server.GetSosReport500JSONResponse(api.StatusInternalServerError(err.Error()))
	}
	switch part.FormName() {
	case sosreport.ReportFormName:
		return s.emitReport(part)
	case sosreport.ErrorFormName:
		return s.emitError(part)
	default:
		return s.emitUnexpected(part)
	}
}

func (s *sosReportGetter) query(ctx context.Context) (ret server.GetSosReportResponseObject, err error) {
	orgId := store.NullOrgId

	device, err := s.store.Device().Get(ctx, orgId, s.request.Name)
	if err != nil {
		switch {
		case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrResourceNameIsNil):
			return server.GetSosReport400JSONResponse(api.StatusBadRequest(err.Error())), nil
		case errors.Is(err, flterrors.ErrResourceNotFound):
			return server.GetSosReport404JSONResponse(api.StatusResourceNotFound("Device", s.request.Name)), nil
		default:
			return nil, err
		}
	}
	if device.Status.Summary.Status == api.DeviceSummaryStatusUnknown {
		return server.GetSosReport503JSONResponse(api.StatusServiceUnavailableError(fmt.Sprintf("device %s is disconnected", s.request.Name))), nil
	}
	id := uuid.New()
	ret, err = s.addSession(ctx, orgId, id)
	defer s.removeSession(ctx, orgId, id)
	if err != nil || ret != nil {
		return ret, err
	}
	timer := time.NewTimer(10 * time.Minute)
	select {
	case reader := <-s.rcvChan:
		return s.getSosReportObject(reader), nil
	case <-timer.C:
		return server.GetSosReport504JSONResponse(api.StatusGatewayTimeoutError("timeout waiting for reader")), nil
	}
}

// (GET /api/v1/devices/{name}/sosreport)
func (h *ServiceHandler) GetSosReport(ctx context.Context, request server.GetSosReportRequestObject) (server.GetSosReportResponseObject, error) {
	return h.sosReportGetter(request).query(ctx)
}
