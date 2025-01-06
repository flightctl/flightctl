package resource

import (
	"fmt"
	"reflect"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
)

func updateMonitor(
	log *log.PrefixLogger,
	monitor *v1alpha1.ResourceMonitor,
	currentSampleInterval *time.Duration,
	alerts map[v1alpha1.ResourceAlertSeverityType]*Alert,
	updateIntervalCh chan time.Duration,
) (bool, error) {
	spec, err := getMonitorSpec(monitor)
	if err != nil {
		return false, err
	}

	newSamplingInterval, err := time.ParseDuration(spec.ResourceMonitorSpec.SamplingInterval)
	if err != nil {
		return false, err
	}

	updated, err := updateAlerts(spec.AlertRules, alerts)
	if err != nil {
		return updated, err
	}

	if *currentSampleInterval != newSamplingInterval {
		log.Infof("Updating sampling interval from %s to %s", *currentSampleInterval, newSamplingInterval)
		updateIntervalCh <- newSamplingInterval
		*currentSampleInterval = newSamplingInterval
		updated = true
	}

	return updated, nil
}

func updateAlerts(newRules []v1alpha1.ResourceAlertRule, existingAlerts map[v1alpha1.ResourceAlertSeverityType]*Alert) (bool, error) {
	updated := false

	// if we had alerts but the rules have been removed clear existing alerts
	if len(newRules) == 0 && len(existingAlerts) > 0 {
		for key := range existingAlerts {
			delete(existingAlerts, key)
		}
		return true, nil
	}

	seen := make(map[v1alpha1.ResourceAlertSeverityType]struct{})
	for _, rule := range newRules {
		seen[rule.Severity] = struct{}{}
	}

	for severity := range existingAlerts {
		if _, ok := seen[severity]; !ok {
			delete(existingAlerts, severity)
			updated = true
		}
	}

	var err error
	for _, rule := range newRules {
		alert, ok := existingAlerts[rule.Severity]
		if !ok {
			alert, err = NewAlert(rule)
			if err != nil {
				return false, err
			}
			existingAlerts[rule.Severity] = alert
			updated = true
		}

		if !reflect.DeepEqual(alert.ResourceAlertRule, rule) {
			if err := alert.UpdateRule(rule); err != nil {
				return false, err
			}
			updated = true
		}
	}

	return updated, nil
}

func getMonitorSpec(monitor *v1alpha1.ResourceMonitor) (*MonitorSpec, error) {
	monitorType, err := monitor.Discriminator()
	if err != nil {
		return nil, err
	}

	switch monitorType {
	case CPUMonitorType:
		spec, err := monitor.AsCpuResourceMonitorSpec()
		if err != nil {
			return nil, err
		}
		return &MonitorSpec{
			ResourceMonitorSpec: v1alpha1.ResourceMonitorSpec{
				SamplingInterval: spec.SamplingInterval,
				AlertRules:       spec.AlertRules,
			},
		}, nil
	case DiskMonitorType:
		spec, err := monitor.AsDiskResourceMonitorSpec()
		if err != nil {
			return nil, err
		}
		return &MonitorSpec{
			ResourceMonitorSpec: v1alpha1.ResourceMonitorSpec{
				SamplingInterval: spec.SamplingInterval,
				AlertRules:       spec.AlertRules,
			},
			Path: spec.Path,
		}, nil
	case MemoryMonitorType:
		spec, err := monitor.AsMemoryResourceMonitorSpec()
		if err != nil {
			return nil, err
		}
		return &MonitorSpec{
			ResourceMonitorSpec: v1alpha1.ResourceMonitorSpec{
				SamplingInterval: spec.SamplingInterval,
				AlertRules:       spec.AlertRules,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unknown monitor type: %s", monitorType)
	}
}
