package device_selection

import (
	"context"
	"crypto/md5" // #nosec G401,G501
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

type querySelectorParts struct {
	fieldSelectorList      []string
	labelSelectorList      []string
	annotationSelectorList []string
}

func newQuerySelectorParts() *querySelectorParts {
	return &querySelectorParts{}
}

func (q *querySelectorParts) listParams() (api.ListDevicesParams, *selector.AnnotationSelector) {
	var ret api.ListDevicesParams
	var annotationSelector *selector.AnnotationSelector

	if len(q.fieldSelectorList) > 0 {
		ret.FieldSelector = lo.ToPtr(strings.Join(q.fieldSelectorList, ","))
	}
	if len(q.labelSelectorList) > 0 {
		ret.LabelSelector = lo.ToPtr(strings.Join(q.labelSelectorList, ","))
	}
	if len(q.annotationSelectorList) > 0 {
		annotationSelector = selector.NewAnnotationSelectorOrDie(strings.Join(q.annotationSelectorList, ","))
	}
	return ret, annotationSelector
}

func (q *querySelectorParts) withSelectedForRollout() *querySelectorParts {
	q.annotationSelectorList = append(q.annotationSelectorList, api.MatchExpression{
		Key:      api.DeviceAnnotationSelectedForRollout,
		Operator: api.Exists,
	}.String())
	return q
}

func (q *querySelectorParts) withOwner(fleetName string) *querySelectorParts {
	q.fieldSelectorList = append(q.fieldSelectorList, "metadata.owner"+"="+util.ResourceOwner(api.FleetKind, fleetName))
	return q
}

func (q *querySelectorParts) withLabelSelector(l *api.LabelSelector) *querySelectorParts {
	if l != nil {
		q.labelSelectorList = append(q.labelSelectorList, lo.MapToSlice(lo.FromPtr(l.MatchLabels), func(k, v string) string { return k + "=" + v })...)
		q.labelSelectorList = append(q.labelSelectorList, lo.Map(lo.FromPtr(l.MatchExpressions), func(e api.MatchExpression, _ int) string { return e.String() })...)
	}
	return q
}

func (q *querySelectorParts) withoutRolledOut(templateVersionName string) *querySelectorParts {
	q.annotationSelectorList = append(q.annotationSelectorList, api.MatchExpression{
		Key:      api.DeviceAnnotationTemplateVersion,
		Operator: api.NotIn,
		Values:   lo.ToPtr([]string{templateVersionName}),
	}.String())
	return q
}

func (q *querySelectorParts) withRolledOut(templateVersionName string) *querySelectorParts {
	q.annotationSelectorList = append(q.annotationSelectorList, api.MatchExpression{
		Key:      api.DeviceAnnotationTemplateVersion,
		Operator: api.In,
		Values:   lo.ToPtr([]string{templateVersionName}),
	}.String())
	return q
}

func (q *querySelectorParts) withoutDisconnected() *querySelectorParts {
	q.fieldSelectorList = append(q.fieldSelectorList, "status.summary.status!=Unknown")
	return q
}

func newBatchSequenceSelector(sequence api.BatchSequence, updateTimeout time.Duration, serviceHandler *service.ServiceHandler, orgId uuid.UUID, fleet *api.Fleet, templateVersionName string, log logrus.FieldLogger) RolloutDeviceSelector {
	return &batchSequenceSelector{
		BatchSequence:       sequence,
		serviceHandler:      serviceHandler,
		orgId:               orgId,
		fleetName:           lo.FromPtr(fleet.Metadata.Name),
		fleet:               fleet,
		templateVersionName: templateVersionName,
		updateTimeout:       updateTimeout,
		log:                 log,
	}
}

type batchSequenceSelector struct {
	api.BatchSequence
	serviceHandler      *service.ServiceHandler
	orgId               uuid.UUID
	fleet               *api.Fleet
	fleetName           string
	templateVersionName string
	updateTimeout       time.Duration
	log                 logrus.FieldLogger
}

func (b *batchSequenceSelector) getAnnotation(annotation string) (string, bool) {
	return util.GetFromMap(lo.FromPtr(b.fleet.Metadata.Annotations), annotation)
}

func (b *batchSequenceSelector) IsRolloutNew() bool {
	dtv, exists := b.getAnnotation(api.FleetAnnotationDeployingTemplateVersion)
	if !exists {
		return true
	}

	// If the deploying template version is not the same as the template version of the fleet,
	// then the rollout is considered new
	return b.templateVersionName != dtv
}

func (b *batchSequenceSelector) getPreviousBatchSequenceDigest() (string, bool) {
	return b.getAnnotation(api.FleetAnnotationDeviceSelectionConfigDigest)
}

func (b *batchSequenceSelector) batchSequenceDigest() (string, error) {
	marshalled, err := json.Marshal(&b.BatchSequence)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", md5.Sum(marshalled)), nil // #nosec G401,G501
}

func (b *batchSequenceSelector) IsDefinitionUpdated() (bool, error) {
	previousBatchSequenceDigest, exists := b.getPreviousBatchSequenceDigest()
	if !exists {
		return true, nil
	}
	currentBatchSequenceDigest, err := b.batchSequenceDigest()
	if err != nil {
		return false, err
	}
	return previousBatchSequenceDigest != currentBatchSequenceDigest, nil
}

func (b *batchSequenceSelector) OnNewRollout(ctx context.Context) error {
	b.log.Infof("%v/%s: OnNewRollout. Template version %s", b.orgId, b.fleetName, b.templateVersionName)
	batchSequenceDigest, err := b.batchSequenceDigest()
	if err != nil {
		return err
	}
	annotations := map[string]string{
		api.FleetAnnotationDeployingTemplateVersion:    b.templateVersionName,
		api.FleetAnnotationDeviceSelectionConfigDigest: batchSequenceDigest,
	}
	return service.ApiStatusToErr(b.serviceHandler.UpdateFleetAnnotations(ctx, b.fleetName, annotations, nil))
}

func (b *batchSequenceSelector) getCurrentBatch(ctx context.Context) (int, error) {
	fleet, status := b.serviceHandler.GetFleet(ctx, b.fleetName, api.GetFleetParams{})
	if status.Code != http.StatusOK {
		return 0, service.ApiStatusToErr(status)
	}
	annotations := lo.FromPtr(fleet.Metadata.Annotations)
	if annotations == nil {
		return 0, fmt.Errorf("couldn't get fleet %s/%s", b.orgId.String(), b.fleetName)
	}
	currentBatchStr, exists := annotations[api.FleetAnnotationBatchNumber]
	if !exists {
		return -1, nil
	}
	currentBatch, err := strconv.ParseInt(currentBatchStr, 10, 64)
	if err != nil {
		return 0, err
	}
	return int(currentBatch), nil
}

func (b *batchSequenceSelector) setCurrentBatch(ctx context.Context, currentBatch int) error {
	b.log.Infof("%v/%s: setCurrentBatch. Batch number %d", b.orgId, b.fleetName, currentBatch)
	annotations := map[string]string{
		api.FleetAnnotationBatchNumber: strconv.FormatInt(int64(currentBatch), 10),
	}
	return service.ApiStatusToErr(b.serviceHandler.UpdateFleetAnnotations(ctx, b.fleetName, annotations, nil))
}

func (b *batchSequenceSelector) HasMoreSelections(ctx context.Context) (bool, error) {
	currentBatch, err := b.getCurrentBatch(ctx)
	if err != nil {
		return false, err
	}
	return currentBatch <= len(lo.FromPtr(b.Sequence)), nil
}

// Advance to the next batch
func (b *batchSequenceSelector) Advance(ctx context.Context) error {
	b.log.Infof("%v/%s:In Advance", b.orgId, b.fleetName)
	currentBatch, err := b.getCurrentBatch(ctx)
	if err != nil {
		return err
	}
	selection, err := b.currentSelection(ctx, currentBatch)
	if err != nil {
		return err
	}

	// Remove the marking for the current batch devices
	if err = selection.unmark(ctx); err != nil {
		return err
	}

	// Indicate that the batch is not approved
	if err = b.clearApproval(ctx); err != nil {
		return err
	}
	nextBatch := currentBatch + 1
	if nextBatch > len(lo.FromPtr(b.Sequence))+1 {
		return fmt.Errorf("batch number overflow")
	}
	selection, err = b.currentSelection(ctx, nextBatch)
	if err != nil {
		return err
	}

	// Save the new batch number in the database
	if err = b.setCurrentBatch(ctx, nextBatch); err != nil {
		return fmt.Errorf("failed to set current batch: %w", err)
	}
	// Mark the devices that participate in the batch
	switch {
	case nextBatch < len(lo.FromPtr(b.Sequence)):
		if err = selection.markBatchSelection(ctx); err != nil {
			return fmt.Errorf("failed to mark devices current batch: %w", err)
		}
	case nextBatch == len(lo.FromPtr(b.Sequence)):
		if err = selection.markRemainingDevices(ctx); err != nil {
			return fmt.Errorf("failed to mark devices current batch: %w", err)
		}
	}

	return nil
}

func (b *batchSequenceSelector) clearApproval(ctx context.Context) error {
	return service.ApiStatusToErr(b.serviceHandler.UpdateFleetAnnotations(ctx, b.fleetName, make(map[string]string), []string{api.FleetAnnotationRolloutApproved}))
}

func (b *batchSequenceSelector) resetApprovalAndCompletionReport(ctx context.Context) error {
	annotations := make(map[string]string)
	if !lo.HasKey(lo.FromPtr(b.fleet.Metadata.Annotations), api.FleetAnnotationRolloutApprovalMethod) {

		// TODO: Return to manual when manual approval will be supported by the UI
		annotations[api.FleetAnnotationRolloutApprovalMethod] = "automatic"
	}
	return service.ApiStatusToErr(b.serviceHandler.UpdateFleetAnnotations(ctx, b.fleetName, annotations, []string{
		api.FleetAnnotationRolloutApproved, api.FleetAnnotationLastBatchCompletionReport}))
}

func (b *batchSequenceSelector) batchName(currentBatch int) string {
	printableBatchNum := currentBatch + 1
	switch {
	case currentBatch == -1:
		return "preliminary batch"
	case currentBatch >= 0 && currentBatch < len(lo.FromPtr(b.Sequence)):
		return fmt.Sprintf("batch %d", printableBatchNum)
	case currentBatch == len(lo.FromPtr(b.Sequence)):
		return "final implicit batch"
	default:
		return fmt.Sprintf("unexpected batch %d", printableBatchNum)
	}
}

func (b *batchSequenceSelector) currentSelection(ctx context.Context, currentBatch int) (*batchSelection, error) {
	var batch *api.Batch
	if currentBatch < -1 || currentBatch > len(lo.FromPtr(b.Sequence))+1 {
		return nil, fmt.Errorf("batch number out of bounds")
	}
	fleet, status := b.serviceHandler.GetFleet(ctx, b.fleetName, api.GetFleetParams{})
	if status.Code != http.StatusOK {
		return nil, service.ApiStatusToErr(status)
	}
	if currentBatch >= 0 && currentBatch < len(lo.FromPtr(b.Sequence)) {
		batch = lo.ToPtr(lo.FromPtr(b.Sequence)[currentBatch])
	}
	batchName := b.batchName(currentBatch)
	return &batchSelection{
		batch:               batch,
		batchNum:            currentBatch,
		batchName:           batchName,
		serviceHandler:      b.serviceHandler,
		orgId:               b.orgId,
		fleetName:           b.fleetName,
		templateVersionName: b.templateVersionName,
		fleet:               fleet,
		updateTimeout:       b.updateTimeout,
		log:                 b.log,
		conditionEmitter:    newConditionEmitter(b.orgId, b.fleetName, batchName, b.serviceHandler),
	}, nil
}

func (b *batchSequenceSelector) CurrentSelection(ctx context.Context) (Selection, error) {
	currentBatch, err := b.getCurrentBatch(ctx)
	if err != nil {
		return nil, err
	}
	return b.currentSelection(ctx, currentBatch)
}

func (b *batchSequenceSelector) UnmarkRolloutSelection(ctx context.Context) error {
	return service.ApiStatusToErr(b.serviceHandler.UnmarkDevicesRolloutSelection(ctx, b.fleetName))
}

func (b *batchSequenceSelector) Reset(ctx context.Context) error {
	b.log.Infof("%v/%s:In Reset", b.orgId, b.fleetName)
	if err := b.UnmarkRolloutSelection(ctx); err != nil {
		return err
	}
	if err := b.resetApprovalAndCompletionReport(ctx); err != nil {
		return err
	}
	return b.setCurrentBatch(ctx, -1)
}

type batchSelection struct {
	batch               *api.Batch
	batchNum            int
	batchName           string
	serviceHandler      *service.ServiceHandler
	orgId               uuid.UUID
	fleetName           string
	templateVersionName string
	fleet               *api.Fleet
	updateTimeout       time.Duration
	conditionEmitter    *conditionEmitter
	log                 logrus.FieldLogger
}

func (b *batchSelection) getAnnotation(annotation string) (string, bool) {
	annotations := lo.FromPtr(b.fleet.Metadata.Annotations)
	if annotations == nil {
		return "", false
	}
	v, exists := annotations[annotation]
	return v, exists
}

func (b *batchSelection) IsApproved() bool {
	approvedStr, exists := b.getAnnotation(api.FleetAnnotationRolloutApproved)
	if !exists {
		return false
	}
	return approvedStr == "true"
}

func (b *batchSelection) IsRolledOut(ctx context.Context) (bool, error) {
	// A rolled out device is one that its template version is the same as the expected template version
	// A rolled out batch is a batch that for every of its devices there is no device that its template
	// version is not equal to the expected template version
	newQuerySelectorParts().withOwner(b.fleetName).withSelectedForRollout().withoutRolledOut(b.templateVersionName).listParams()
	listParams, annotationSelector := newQuerySelectorParts().
		withOwner(b.fleetName).
		withSelectedForRollout().
		withoutRolledOut(b.templateVersionName).
		listParams()
	count, status := b.serviceHandler.CountDevices(ctx, listParams, annotationSelector)
	if status.Code != http.StatusOK {
		return false, service.ApiStatusToErr(status)
	}
	return count == 0, nil
}

func (b *batchSelection) getSuccessThreshold() (int, error) {
	var (
		ret int
		err error
	)
	successThreshold := b.fleet.Spec.RolloutPolicy.SuccessThreshold
	if b.batch != nil && b.batch.SuccessThreshold != nil {
		successThreshold = b.batch.SuccessThreshold
	}
	if successThreshold != nil {
		ret, err = api.PercentageAsInt(*successThreshold)
		if err != nil {
			return 0, err
		}
	} else {
		ret = DefaultSuccessThreshold
	}
	return ret, nil
}

func (b *batchSelection) getLastCompletionReport() (CompletionReport, bool, error) {
	var report CompletionReport
	completionReportStr, exists := b.getAnnotation(api.FleetAnnotationLastBatchCompletionReport)
	if !exists {
		return report, false, nil
	}
	if err := json.Unmarshal([]byte(completionReportStr), &report); err != nil {
		return report, false, fmt.Errorf("failed to unmarshal completion report: %w", err)
	}
	return report, true, nil
}

func (b *batchSelection) getLastSuccessPercentage() (int, bool, error) {
	report, exists, err := b.getLastCompletionReport()
	if err != nil || !exists {
		return 0, exists, err
	}
	return int(report.SuccessPercentage), true, nil
}

func (b *batchSelection) isApprovalMethodAutomatic() bool {
	approvalMethod, _ := b.getAnnotation(api.FleetAnnotationRolloutApprovalMethod)
	return approvalMethod == "automatic"
}

// A batch may be approved atotmatically only if its approval method is "automatic" and the
// success percentage of the previous batch is greater or equal to the success threshold
func (b *batchSelection) MayApproveAutomatically() (bool, error) {
	if b.batchNum == -1 {
		return true, nil
	}
	if !b.isApprovalMethodAutomatic() {
		return false, nil
	}
	successThreshold, err := b.getSuccessThreshold()
	if err != nil {
		return false, err
	}
	lastSuccessPercentage, exists, err := b.getLastSuccessPercentage()
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}

	return lastSuccessPercentage >= successThreshold, nil
}

func (b *batchSelection) Approve(ctx context.Context) error {
	b.log.Infof("%v/%s:In Approve", b.orgId, b.fleetName)
	annotations := map[string]string{
		api.FleetAnnotationRolloutApproved: "true",
	}
	return service.ApiStatusToErr(b.serviceHandler.UpdateFleetAnnotations(ctx, b.fleetName, annotations, nil))
}

// A group of device is considered as completed successfully if the rendered template version is the same as the
// template version of the fleet and same-rendered-version is true
func (b *batchSelection) isUpdateCompletedSuccessfully(c api.DeviceCompletionCount) bool {
	return c.SameTemplateVersion && c.SameRenderedVersion
}

func (b *batchSelection) isFailed(c api.DeviceCompletionCount) bool {
	return c.SameTemplateVersion && c.UpdatingReason == api.UpdateStateError
}

func (b *batchSelection) isTimedOut(c api.DeviceCompletionCount) bool {
	return c.SameTemplateVersion && c.UpdateTimedOut
}

// IsComplete checks is the total number of devices in a batch is the same as the number of completed
func (b *batchSelection) IsComplete(ctx context.Context) (bool, error) {
	counts, status := b.serviceHandler.GetDeviceCompletionCounts(ctx, util.ResourceOwner(api.FleetKind, b.fleetName), b.templateVersionName, &b.updateTimeout)
	if status.Code != http.StatusOK {
		return false, service.ApiStatusToErr(status)
	}

	// A device is counted in total if it has completed successfully, or it is in update.
	total := lo.Sum(lo.Map(counts, func(c api.DeviceCompletionCount, _ int) int64 {
		return c.Count
	}))

	// A device is counted as completed if it has completed successfully or, it is in error state or its update is timed out
	complete := lo.Sum(lo.Map(counts, func(c api.DeviceCompletionCount, _ int) int64 {
		return lo.Ternary(b.isUpdateCompletedSuccessfully(c) || c.SameTemplateVersion && (c.UpdatingReason == api.UpdateStateError || c.UpdateTimedOut), c.Count, 0)
	}))
	return total == complete, nil
}

type CompletionReport struct {
	BatchName         string `json:"batchName"`
	SuccessPercentage int64  `json:"successPercentage"`
	Total             int64  `json:"total"`
	Successful        int64  `json:"successful"`
	Failed            int64  `json:"failed"`
	TimedOut          int64  `json:"timedOut"`
}

func (b *batchSelection) completionReport(counts []api.DeviceCompletionCount) CompletionReport {
	ret := CompletionReport{
		BatchName: b.batchName,
	}

	for _, c := range counts {
		ret.Total += c.Count
		if b.isUpdateCompletedSuccessfully(c) {
			ret.Successful += c.Count
		} else if b.isFailed(c) {
			ret.Failed += c.Count
		} else if b.isTimedOut(c) {
			ret.TimedOut += c.Count
		}
	}
	ret.SuccessPercentage = 100
	if ret.Total != 0 {
		ret.SuccessPercentage = ret.Successful * 100 / ret.Total
	}
	return ret
}

func (b *batchSelection) SetCompletionReport(ctx context.Context) error {
	counts, status := b.serviceHandler.GetDeviceCompletionCounts(ctx, util.ResourceOwner(api.FleetKind, b.fleetName), b.templateVersionName, &b.updateTimeout)
	if status.Code != http.StatusOK {
		return service.ApiStatusToErr(status)
	}

	report := b.completionReport(counts)
	if report.Total == 0 {
		return nil
	}
	out, err := json.Marshal(&report)
	if err != nil {
		return fmt.Errorf("failed to marshal completion report: %w", err)
	}
	outStr := string(out)
	b.log.Infof("%v/%s:In SetCompletionReport: %s", b.orgId, b.fleetName, outStr)
	annotations := map[string]string{
		api.FleetAnnotationLastBatchCompletionReport: outStr,
	}
	return service.ApiStatusToErr(b.serviceHandler.UpdateFleetAnnotations(ctx, b.fleetName, annotations, nil))
}

func (b *batchSelection) batchCounts(ctx context.Context) (int, int, error) {
	parts := newQuerySelectorParts().
		withOwner(b.fleetName).
		withLabelSelector(b.batch.Selector)
	listParams, annotationSelector := parts.listParams()
	total, status := b.serviceHandler.CountDevices(ctx, listParams, annotationSelector)
	if status.Code != http.StatusOK {
		return 0, 0, service.ApiStatusToErr(status)
	}
	listParams, annotationSelector = parts.withRolledOut(b.templateVersionName).listParams()
	rolledOut, status := b.serviceHandler.CountDevices(ctx, listParams, annotationSelector)
	if status.Code != http.StatusOK {
		return 0, 0, service.ApiStatusToErr(status)
	}
	return int(rolledOut), int(total), nil
}

func (b *batchSelection) calculateLimit(ctx context.Context) (*int, error) {
	if b.batch == nil || b.batch.Limit == nil {
		return nil, nil
	}
	unifiedBatchLimit := *b.batch.Limit
	intBatchLimit, intErr := unifiedBatchLimit.AsBatchLimit1()
	if intErr == nil {
		return &intBatchLimit, nil
	}
	percentageStr, pErr := unifiedBatchLimit.AsPercentage()
	if pErr != nil {
		return nil, errors.Join(intErr, pErr)
	}
	percentage, err := api.PercentageAsInt(percentageStr)
	if err != nil {
		return nil, err
	}
	if percentage == 100 {
		// Ignore limit
		return nil, nil
	}
	rolledOut, total, err := b.batchCounts(ctx)
	if err != nil {
		return nil, err
	}
	res := int(math.Round(float64(total)*float64(percentage)/100.0)) - rolledOut
	return &res, nil
}

func (b *batchSelection) unmark(ctx context.Context) error {
	return service.ApiStatusToErr(b.serviceHandler.UnmarkDevicesRolloutSelection(ctx, b.fleetName))
}

func (b *batchSelection) markBatchSelection(ctx context.Context) error {
	if b.batch == nil {
		return nil
	}
	limit, err := b.calculateLimit(ctx)
	if err != nil {
		return err
	}
	if limit != nil && *limit <= 0 {
		// Limit already reached.  Do not mark any device for rollout
		return nil
	}
	listParams, annotationSelector := newQuerySelectorParts().
		withOwner(b.fleetName).
		withLabelSelector(b.batch.Selector).
		withoutRolledOut(b.templateVersionName).
		withoutDisconnected().
		listParams()
	return service.ApiStatusToErr(b.serviceHandler.MarkDevicesRolloutSelection(ctx, listParams, annotationSelector, limit))
}

func (b *batchSelection) markRemainingDevices(ctx context.Context) error {
	listParams, annotationSelector := newQuerySelectorParts().
		withOwner(b.fleetName).
		withoutRolledOut(b.templateVersionName).
		listParams()
	return service.ApiStatusToErr(b.serviceHandler.MarkDevicesRolloutSelection(ctx, listParams, annotationSelector, nil))
}

func (b *batchSelection) Devices(ctx context.Context) (*api.DeviceList, error) {
	listParams, annotationSelector := newQuerySelectorParts().
		withOwner(b.fleetName).
		withSelectedForRollout().
		listParams()
	result, status := b.serviceHandler.ListDevices(ctx, listParams, annotationSelector)
	return result, service.ApiStatusToErr(status)
}

func (b *batchSelection) OnRollout(ctx context.Context) error {
	return b.conditionEmitter.active(ctx)
}

func (b *batchSelection) OnSuspended(ctx context.Context) error {
	if b.isApprovalMethodAutomatic() {
		report, exists, err := b.getLastCompletionReport()
		if err != nil {
			return fmt.Errorf("failed to get last completion report: %w", err)
		}
		if !exists {
			return fmt.Errorf("last completion report doesn't exist")
		}
		successThreshold, err := b.getSuccessThreshold()
		if err != nil {
			return fmt.Errorf("failed to get succuess threshold: %w", err)
		}
		return b.conditionEmitter.suspended(ctx, successThreshold, report)
	} else {
		return b.conditionEmitter.waiting(ctx)
	}
}

func (b *batchSelection) OnFinish(ctx context.Context) error {
	return b.conditionEmitter.inactive(ctx)
}
