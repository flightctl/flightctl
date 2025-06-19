package alert_exporter

import (
	"github.com/sirupsen/logrus"
)

type AlertSender struct {
	log                *logrus.Logger
	alertmanagerClient *AlertmanagerClient
}

func NewAlertSender(log *logrus.Logger, hostname string, port uint) *AlertSender {
	return &AlertSender{
		log:                log,
		alertmanagerClient: NewAlertmanagerClient(hostname, port),
	}
}

func (a *AlertSender) SendAlerts(checkpoint *AlertCheckpoint) error {
	err := a.alertmanagerClient.SendAllAlerts(checkpoint.Alerts)
	if err != nil {
		return err
	}

	a.cleanupAlerts(checkpoint)
	return nil
}

// Remove alerts that have been resolved (endedAt != nil)
func (a *AlertSender) cleanupAlerts(checkpoint *AlertCheckpoint) {
	for i, alerts := range checkpoint.Alerts {
		for _, alert := range alerts {
			if alert.EndsAt != nil {
				delete(alerts, alert.Reason)
			}
		}
		if len(alerts) == 0 {
			delete(checkpoint.Alerts, i)
		}
	}
}
