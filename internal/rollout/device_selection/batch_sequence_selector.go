package device_selection

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const (
	DefaultSuccessThreshold = 90
	Unknown                 = "Unknown"
	ApplyingUpdate          = "ApplyingUpdate"
	Rebooting               = "Rebooting"
	ActivatingConfig        = "ActivatingConfig"
	RollingBack             = "RollingBack"
	Error                   = "Error"
)

func newBatchSequenceSelector(sequence v1alpha1.BatchSequence, defaultUpdateTimeout *time.Duration, store store.Store, orgId uuid.UUID, fleet *v1alpha1.Fleet, templateVersionName string, log logrus.FieldLogger) RolloutDeviceSelector {
	return &batchSequenceSelector{
		BatchSequence:        sequence,
		store:                store,
		orgId:                orgId,
		fleetName:            lo.FromPtr(fleet.Metadata.Name),
		fleet:                fleet,
		templateVersionName:  templateVersionName,
		defaultUpdateTimeout: defaultUpdateTimeout,
		log:                  log,
	}
}

type batchSequenceSelector struct {
	v1alpha1.BatchSequence
	store                store.Store
	orgId                uuid.UUID
	fleet                *v1alpha1.Fleet
	fleetName            string
	templateVersionName  string
	defaultUpdateTimeout *time.Duration
	log                  logrus.FieldLogger
}

func (b *batchSequenceSelector) getAnnotation(annotation string) (string, bool) {
	annotations := lo.FromPtr(b.fleet.Metadata.Annotations)
	if annotations == nil {
		return "", false
	}
	v, exists := annotations[annotation]
	return v, exists
}

func (b *batchSequenceSelector) IsRolloutNew() bool {
	dtv, exists := b.getAnnotation(v1alpha1.FleetAnnotationDeployingTemplateVersion)
	if !exists {
		return true
	}
	return b.templateVersionName != dtv
}

func (b *batchSequenceSelector) OnNewRollout(ctx context.Context) error {
	b.log.Infof("%v/%s: OnNewRollout. Template version %s", b.orgId, b.fleetName, b.templateVersionName)
	annotations := map[string]string{
		v1alpha1.FleetAnnotationDeployingTemplateVersion: b.templateVersionName,
	}
	return b.store.Fleet().UpdateAnnotations(ctx, b.orgId, b.fleetName, annotations, nil)
}

func (b *batchSequenceSelector) getCurrentBatch(ctx context.Context) (int, error) {
	fleet, err := b.store.Fleet().Get(ctx, b.orgId, b.fleetName)
	if err != nil {
		return 0, err
	}
	annotations := lo.FromPtr(fleet.Metadata.Annotations)
	if annotations == nil {
		return 0, fmt.Errorf("couldn't get fleet %s/%s", b.orgId.String(), b.fleetName)
	}
	currentBatchStr, exists := annotations[v1alpha1.FleetAnnotationBatchNumber]
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
		v1alpha1.FleetAnnotationBatchNumber: strconv.FormatInt(int64(currentBatch), 10),
	}
	return b.store.Fleet().UpdateAnnotations(ctx, b.orgId, b.fleetName, annotations, nil)
}

func (b *batchSequenceSelector) HasMoreSelections(ctx context.Context) (bool, error) {
	currentBatch, err := b.getCurrentBatch(ctx)
	if err != nil {
		return false, err
	}
	return currentBatch < len(lo.FromPtr(b.Sequence)), nil
}

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
	if err = selection.unmark(ctx); err != nil {
		return err
	}
	nextBatch := currentBatch + 1
	if nextBatch > len(lo.FromPtr(b.Sequence)) {
		return fmt.Errorf("batch number overflow")
	}
	selection, err = b.currentSelection(ctx, nextBatch)
	if err != nil {
		return err
	}
	if err = b.setCurrentBatch(ctx, nextBatch); err != nil {
		return fmt.Errorf("failed to set current batch: %v", err)
	}
	if err = selection.mark(ctx); err != nil {
		return err
	}
	return b.clearApproval(ctx)
}

func (b *batchSequenceSelector) clearApproval(ctx context.Context) error {
	return b.store.Fleet().UpdateAnnotations(ctx, b.orgId, b.fleetName, make(map[string]string), []string{v1alpha1.FleetAnnotationRolloutApproved})
}

func (b *batchSequenceSelector) resetApproval(ctx context.Context) error {
	annotations := map[string]string{
		v1alpha1.FleetAnnotationRolloutApprovalMethod: "manual",
	}
	return b.store.Fleet().UpdateAnnotations(ctx, b.orgId, b.fleetName, annotations, []string{v1alpha1.FleetAnnotationRolloutApproved})
}

func (b *batchSequenceSelector) currentSelection(ctx context.Context, currentBatch int) (*batchSelection, error) {
	var batch *v1alpha1.Batch
	if currentBatch < -1 || currentBatch > len(lo.FromPtr(b.Sequence)) {
		return nil, fmt.Errorf("batch number out of bounds")
	}
	fleet, err := b.store.Fleet().Get(ctx, b.orgId, b.fleetName)
	if err != nil {
		return nil, err
	}
	if currentBatch >= 0 && currentBatch < len(lo.FromPtr(b.Sequence)) {
		batch = lo.ToPtr(lo.FromPtr(b.Sequence)[currentBatch])
	}
	return &batchSelection{
		batch:                batch,
		store:                b.store,
		orgId:                b.orgId,
		fleetName:            b.fleetName,
		templateVersionName:  b.templateVersionName,
		fleet:                fleet,
		defaultUpdateTimeout: b.defaultUpdateTimeout,
		log:                  b.log,
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
	return b.store.Device().UnmarkRolloutSelection(ctx, b.orgId, b.fleetName)
}

func (b *batchSequenceSelector) Reset(ctx context.Context) error {
	b.log.Infof("%v/%s:In Reset", b.orgId, b.fleetName)
	if err := b.UnmarkRolloutSelection(ctx); err != nil {
		return err
	}
	if err := b.resetApproval(ctx); err != nil {
		return err
	}
	return b.setCurrentBatch(ctx, -1)
}

type batchSelection struct {
	batch                *v1alpha1.Batch
	store                store.Store
	orgId                uuid.UUID
	fleetName            string
	templateVersionName  string
	fleet                *v1alpha1.Fleet
	defaultUpdateTimeout *time.Duration
	log                  logrus.FieldLogger
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
	approvedStr, exists := b.getAnnotation(v1alpha1.FleetAnnotationRolloutApproved)
	if !exists {
		return false
	}
	return approvedStr == "true"
}

func (b *batchSelection) IsRolledOut(ctx context.Context) (bool, error) {
	count, err := b.store.Device().Count(ctx, b.orgId, store.ListParams{
		FieldSelector: selector.NewFieldSelectorFromMapOrDie(map[string]string{"metadata.owner": util.ResourceOwner(v1alpha1.FleetKind, b.fleetName)}, false),
		AnnotationSelector: selector.NewAnnotationSelectorOrDie(strings.Join([]string{v1alpha1.MatchExpression{
			Operator: v1alpha1.NotIn,
			Key:      v1alpha1.DeviceAnnotationTemplateVersion,
			Values:   &[]string{b.templateVersionName},
		}.String(),
			v1alpha1.MatchExpression{
				Key:      v1alpha1.DeviceAnnotationSelectedForRollout,
				Operator: v1alpha1.Exists,
			}.String()}, ",")),
	})
	if err != nil {
		return false, err
	}
	return count == 0, nil
}

func (b *batchSelection) getSuccessThreshold() (int, error) {
	var (
		ret int
		err error
	)
	successThreshold := lo.Ternary(b.batch.SuccessThreshold != nil, b.batch.SuccessThreshold, b.fleet.Spec.RolloutPolicy.SuccessThreshold)
	if successThreshold != nil {
		ret, err = v1alpha1.PercentageAsInt(*successThreshold)
		if err != nil {
			return 0, err
		}
	} else {
		ret = DefaultSuccessThreshold
	}
	return ret, nil
}

func (b *batchSelection) getLastSuccessPercentage() (int, bool, error) {
	percentageStr, exists := b.getAnnotation(v1alpha1.FleetAnnotationLastBatchSuccessPercentage)
	if !exists {
		return 0, false, nil
	}
	ret, err := strconv.ParseInt(percentageStr, 10, 64)
	if err != nil {
		return 0, false, err
	}
	return int(ret), true, nil
}

func (b *batchSelection) MayApproveAutomatically() (bool, error) {
	approvalMethod, exists := b.getAnnotation(v1alpha1.FleetAnnotationRolloutApprovalMethod)
	if !exists || approvalMethod != "automatic" {
		return false, nil
	}
	successThreshold, err := b.getSuccessThreshold()
	if err != nil {
		return false, nil
	}
	lastSuccessPercentage, exists, err := b.getLastSuccessPercentage()
	if err != nil || !exists {
		return false, err
	}

	return lastSuccessPercentage >= successThreshold, nil
}

func (b *batchSelection) Approve(ctx context.Context) error {
	b.log.Infof("%v/%s:In Approve", b.orgId, b.fleetName)
	annotations := map[string]string{
		v1alpha1.FleetAnnotationRolloutApproved: "true",
	}
	return b.store.Fleet().UpdateAnnotations(ctx, b.orgId, b.fleetName, annotations, nil)
}

func isInUpdate(reason string) bool {
	return lo.Contains([]string{Rebooting, ActivatingConfig, ApplyingUpdate, RollingBack}, reason)
}

// A group of device is considered as completed successfully if the rendered template version is the same as the
// template version of the fleet and same-rendered-version is true
func (b *batchSelection) isUpdateCompletedSuccessfully(c store.CompletionCount) bool {
	return c.RenderedTemplateVersion == b.templateVersionName && c.SameRenderedVersion
}

// IsComplete checks is the total number of devices in a batch is the same as the number of completed
func (b *batchSelection) IsComplete(ctx context.Context) (bool, error) {
	if b.batch == nil {
		return true, nil
	}

	counts, err := b.store.Device().CompletionCounts(ctx, b.orgId, util.ResourceOwner(v1alpha1.FleetKind, b.fleetName), b.defaultUpdateTimeout)
	if err != nil {
		return false, err
	}

	// A device is counted in total if it has completed successfully, or it is not disconnected (Unknown), or it is in update.
	total := lo.Sum(lo.Map(counts, func(c store.CompletionCount, _ int) int64 {
		return lo.Ternary(b.isUpdateCompletedSuccessfully(c) || c.SummaryStatus != Unknown || isInUpdate(c.UpdatingReason), c.Count, 0)
	}))

	// A device is counted as completed if it has completed successfully or, it is not disconnected, and it is in error state or its update is timed out
	complete := lo.Sum(lo.Map(counts, func(c store.CompletionCount, _ int) int64 {
		return lo.Ternary(b.isUpdateCompletedSuccessfully(c) || c.SummaryStatus != Unknown && (c.UpdatingReason == Error || c.UpdateTimedOut && c.RenderedTemplateVersion == b.templateVersionName), c.Count, 0)
	}))
	return total == complete, nil
}

func (b *batchSelection) SetSuccessPercentage(ctx context.Context) error {
	if b.batch == nil {
		return nil
	}
	counts, err := b.store.Device().CompletionCounts(ctx, b.orgId, util.ResourceOwner(v1alpha1.FleetKind, b.fleetName), b.defaultUpdateTimeout)
	if err != nil {
		return err
	}
	total := lo.Sum(lo.Map(counts, func(c store.CompletionCount, _ int) int64 {
		return lo.Ternary(b.isUpdateCompletedSuccessfully(c) || c.SummaryStatus != Unknown || isInUpdate(c.UpdatingReason), c.Count, 0)
	}))
	successful := lo.Sum(lo.Map(counts, func(c store.CompletionCount, _ int) int64 {
		return lo.Ternary(b.isUpdateCompletedSuccessfully(c), c.Count, 0)
	}))
	if total == 0 {
		return nil
	}
	successPercentage := successful * 100 / total
	b.log.Infof("%v/%s:In SetSuccessPercentage: %d", b.orgId, b.fleetName, successPercentage)
	annotations := map[string]string{
		v1alpha1.FleetAnnotationLastBatchSuccessPercentage: strconv.FormatInt(successPercentage, 10),
	}
	return b.store.Fleet().UpdateAnnotations(ctx, b.orgId, b.fleetName, annotations, nil)
}

func (b *batchSelection) labelSelector() store.ListParams {
	ret := store.ListParams{
		FieldSelector: selector.NewFieldSelectorFromMapOrDie(map[string]string{"metadata.owner": util.ResourceOwner(v1alpha1.FleetKind, b.fleetName)}, false),
	}
	if b.batch != nil && b.batch.Selector != nil {
		labelSelectorList := lo.MapToSlice(lo.FromPtr(b.batch.Selector.MatchLabels), func(k, v string) string { return k + "=" + v })
		labelSelectorList = append(labelSelectorList, lo.Map(lo.FromPtr(b.batch.Selector.MatchExpressions), func(e v1alpha1.MatchExpression, _ int) string { return e.String() })...)
		ret.LabelSelector = selector.NewLabelSelectorOrDie(strings.Join(labelSelectorList, ","))
	}
	return ret
}

func (b *batchSelection) notRolledOutSelector() store.ListParams {
	ret := b.labelSelector()
	ret.AnnotationSelector = selector.NewAnnotationSelectorOrDie(v1alpha1.MatchExpression{
		Key:      v1alpha1.DeviceAnnotationTemplateVersion,
		Operator: v1alpha1.NotIn,
		Values:   lo.ToPtr([]string{b.templateVersionName}),
	}.String())
	return ret
}

func (b *batchSelection) rolledOutSelector() store.ListParams {
	ret := b.labelSelector()
	ret.AnnotationSelector = selector.NewAnnotationSelectorOrDie(v1alpha1.MatchExpression{
		Key:      v1alpha1.DeviceAnnotationTemplateVersion,
		Operator: v1alpha1.In,
		Values:   lo.ToPtr([]string{b.templateVersionName}),
	}.String())
	return ret
}

func (b *batchSelection) batchCounts(ctx context.Context) (int, int, error) {
	total, err := b.store.Device().Count(ctx, b.orgId, b.labelSelector())
	if err != nil {
		return 0, 0, err
	}
	rolledOut, err := b.store.Device().Count(ctx, b.orgId, b.rolledOutSelector())
	if err != nil {
		return 0, 0, err
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
	percentage, err := v1alpha1.PercentageAsInt(percentageStr)
	if err != nil {
		return nil, err
	}
	rolledOut, total, err := b.batchCounts(ctx)
	if err != nil {
		return nil, err
	}
	res := int(math.Round(float64(total)*float64(percentage)/100.0)) - rolledOut
	return &res, nil
}

func (b *batchSelection) unmark(ctx context.Context) error {
	return b.store.Device().UnmarkRolloutSelection(ctx, b.orgId, b.fleetName)
}

func (b *batchSelection) mark(ctx context.Context) error {
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
	return b.store.Device().MarkRolloutSelection(ctx, b.orgId, b.notRolledOutSelector(), limit)
}

func (b *batchSelection) Devices(ctx context.Context) (*v1alpha1.DeviceList, error) {
	return b.store.Device().List(ctx, b.orgId, store.ListParams{
		FieldSelector: selector.NewFieldSelectorFromMapOrDie(map[string]string{
			"metadata.owner": util.ResourceOwner(v1alpha1.FleetKind, b.fleetName),
		}, false),
		AnnotationSelector: selector.NewAnnotationSelectorOrDie(v1alpha1.MatchExpression{
			Key:      v1alpha1.DeviceAnnotationSelectedForRollout,
			Operator: v1alpha1.Exists,
		}.String()),
	})
}
