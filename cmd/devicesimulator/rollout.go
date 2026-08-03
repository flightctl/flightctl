package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	apiClient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

// fleetTemplate aliases the anonymous Template struct on FleetSpec.
type fleetTemplate = struct {
	Metadata *v1beta1.ObjectMeta `json:"metadata,omitempty"`
	Spec     v1beta1.DeviceSpec  `json:"spec"`
}

func runRollout(ctx context.Context, log *logrus.Logger, serviceClient *apiClient.ClientWithResponses, fleetPrefix string, fleetCount int, templatePath string, timeout time.Duration) error {
	if fleetCount <= 0 {
		return fmt.Errorf("--rollout requires --fleet-count > 0")
	}
	if templatePath == "" {
		return fmt.Errorf("--rollout requires --rollout-template")
	}

	template, err := loadFleetTemplate(templatePath)
	if err != nil {
		return err
	}

	fleetNames := scaleFleetNames(fleetPrefix, fleetCount)
	for _, name := range fleetNames {
		if err := applyFleetTemplate(ctx, log, serviceClient, name, template); err != nil {
			return err
		}
	}

	log.Infof("waiting up to %s for rollout convergence across %d fleets", timeout, len(fleetNames))
	log.Infoln("note: devices stuck at OutOfDate may reproduce the known non-atomic fleet rollout render race")

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(cleanupProgressInterval)
	defer ticker.Stop()

	for {
		allUpToDate, summaries, err := pollRolloutStatus(ctx, serviceClient, fleetNames)
		if err != nil {
			return err
		}
		for _, summary := range summaries {
			log.Infof("fleet %s: upToDate=%d updating=%d outOfDate=%d unknown=%d total=%d",
				summary.name, summary.upToDate, summary.updating, summary.outOfDate, summary.unknown, summary.total)
		}
		if allUpToDate {
			log.Infoln("rollout complete: all devices report UpToDate")
			return nil
		}
		if time.Now().After(deadline) {
			log.Warnln("rollout timed out before full convergence")
			for _, summary := range summaries {
				log.Warnf("final fleet %s: upToDate=%d updating=%d outOfDate=%d unknown=%d total=%d",
					summary.name, summary.upToDate, summary.updating, summary.outOfDate, summary.unknown, summary.total)
			}
			return fmt.Errorf("rollout timed out after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func loadFleetTemplate(path string) (fleetTemplate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return fleetTemplate{}, fmt.Errorf("reading rollout template %s: %w", path, err)
	}
	var fleet v1beta1.Fleet
	if err := yaml.Unmarshal(data, &fleet); err != nil {
		return fleetTemplate{}, fmt.Errorf("unmarshaling rollout template: %w", err)
	}
	return fleet.Spec.Template, nil
}

func applyFleetTemplate(ctx context.Context, log *logrus.Logger, serviceClient *apiClient.ClientWithResponses, name string, template fleetTemplate) error {
	getResp, err := serviceClient.GetFleetWithResponse(ctx, name, &v1beta1.GetFleetParams{})
	if err != nil {
		return fmt.Errorf("get fleet %s: %w", name, err)
	}
	if getResp.StatusCode() != http.StatusOK || getResp.JSON200 == nil {
		return fmt.Errorf("get fleet %s: status %d", name, getResp.StatusCode())
	}
	fleet := *getResp.JSON200
	fleet.Spec.Template = template
	replaceResp, err := serviceClient.ReplaceFleetWithResponse(ctx, name, fleet)
	if err != nil {
		return fmt.Errorf("replace fleet %s: %w", name, err)
	}
	if replaceResp.StatusCode() < http.StatusOK || replaceResp.StatusCode() >= http.StatusMultipleChoices {
		return fmt.Errorf("replace fleet %s: status %d, body: %s", name, replaceResp.StatusCode(), string(replaceResp.Body))
	}
	log.Infof("updated fleet template for %s", name)
	return nil
}

type fleetRolloutSummary struct {
	name      string
	upToDate  int
	updating  int
	outOfDate int
	unknown   int
	total     int
}

func pollRolloutStatus(ctx context.Context, serviceClient *apiClient.ClientWithResponses, fleetNames []string) (bool, []fleetRolloutSummary, error) {
	summaries := make([]fleetRolloutSummary, 0, len(fleetNames))
	allUpToDate := true
	for _, name := range fleetNames {
		summary, err := tallyFleetDevices(ctx, serviceClient, name)
		if err != nil {
			return false, nil, err
		}
		summaries = append(summaries, summary)
		updateRolloutMetrics(summary)
		if summary.total == 0 || summary.upToDate != summary.total {
			allUpToDate = false
		}
	}
	return allUpToDate, summaries, nil
}

func tallyFleetDevices(ctx context.Context, serviceClient *apiClient.ClientWithResponses, fleetName string) (fleetRolloutSummary, error) {
	summary := fleetRolloutSummary{name: fleetName}
	selector := fmt.Sprintf("fleet=%s", fleetName)
	limit := cleanupListLimit
	var continueToken *string

	for {
		resp, err := serviceClient.ListDevicesWithResponse(ctx, &v1beta1.ListDevicesParams{
			LabelSelector: &selector,
			Limit:         &limit,
			Continue:      continueToken,
		})
		if err != nil {
			return summary, fmt.Errorf("list devices for fleet %s: %w", fleetName, err)
		}
		if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
			return summary, fmt.Errorf("list devices for fleet %s: status %d", fleetName, resp.StatusCode())
		}
		for _, device := range resp.JSON200.Items {
			summary.total++
			if device.Status == nil {
				summary.unknown++
				continue
			}
			switch device.Status.Updated.Status {
			case v1beta1.DeviceUpdatedStatusUpToDate:
				summary.upToDate++
			case v1beta1.DeviceUpdatedStatusUpdating:
				summary.updating++
			case v1beta1.DeviceUpdatedStatusOutOfDate:
				summary.outOfDate++
			default:
				summary.unknown++
			}
		}
		if resp.JSON200.Metadata.Continue == nil || *resp.JSON200.Metadata.Continue == "" {
			break
		}
		continueToken = resp.JSON200.Metadata.Continue
	}
	return summary, nil
}

func updateRolloutMetrics(summary fleetRolloutSummary) {
	rolloutDevicesByStatus.WithLabelValues(summary.name, string(v1beta1.DeviceUpdatedStatusUpToDate)).Set(float64(summary.upToDate))
	rolloutDevicesByStatus.WithLabelValues(summary.name, string(v1beta1.DeviceUpdatedStatusUpdating)).Set(float64(summary.updating))
	rolloutDevicesByStatus.WithLabelValues(summary.name, string(v1beta1.DeviceUpdatedStatusOutOfDate)).Set(float64(summary.outOfDate))
	rolloutDevicesByStatus.WithLabelValues(summary.name, string(v1beta1.DeviceUpdatedStatusUnknown)).Set(float64(summary.unknown))
}
