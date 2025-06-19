package alert_exporter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const SentinelAlertName = "__sentinel_alert__"
const batchSize = 100

type AlertmanagerClient struct {
	hostname string
	port     uint
}

type AlertmanagerAlert struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt,omitempty"`
	GeneratorURL string            `json:"generatorURL,omitempty"`
}

func NewAlertmanagerClient(hostname string, port uint) *AlertmanagerClient {
	return &AlertmanagerClient{
		hostname: hostname,
		port:     port,
	}
}

// SendAllAlerts sends all alerts from a nested map to Alertmanager in batches.
func (a *AlertmanagerClient) SendAllAlerts(alerts map[AlertKey]map[string]*AlertInfo) error {
	alertBatch := make([]AlertmanagerAlert, 0, len(alerts))

	for _, alerts := range alerts {
		for _, alert := range alerts {
			alertBatch = append(alertBatch, alertToAlertmanagerAlert(alert))

			// Send the batch if it's full
			if len(alertBatch) >= batchSize {
				err := a.postBatch(alertBatch)
				if err != nil {
					return fmt.Errorf("failed to send alerts: %v", err)
				}
				alertBatch = alertBatch[:0] // reset
			}
		}
	}

	// Send any remaining alerts
	if len(alertBatch) > 0 {
		err := a.postBatch(alertBatch)
		if err != nil {
			return fmt.Errorf("failed to send alerts: %v", err)
		}
	}

	return nil
}

// Helper function to post a batch of alerts
func (a *AlertmanagerClient) postBatch(batch []AlertmanagerAlert) error {
	body, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("failed to marshal alerts: %v", err)
	}

	url := fmt.Sprintf("http://%s:%d/api/v2/alerts", a.hostname, a.port)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))

	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send alerts: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("alertmanager returned status %s", resp.Status)
	}
	return nil
}

func alertToAlertmanagerAlert(alert *AlertInfo) AlertmanagerAlert {
	alertmanagerAlert := AlertmanagerAlert{
		Labels: map[string]string{
			"alertname": alert.Reason,
			"resource":  alert.ResourceName,
			"org_id":    alert.OrgID,
		},
		StartsAt: alert.StartsAt,
	}
	if alert.EndsAt != nil {
		alertmanagerAlert.EndsAt = *alert.EndsAt
	}
	return alertmanagerAlert
}
