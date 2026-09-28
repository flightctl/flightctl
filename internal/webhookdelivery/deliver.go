package webhookdelivery

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

const maxResponseBodySize = 4096 // 4 KiB cap for empty-body validation

// Delivery is a single outbound webhook POST.
type Delivery struct {
	Config      Config
	BearerToken string
	Payload     []byte
	DeliveryID  string
}

// NewClient creates an HTTP client configured for webhook delivery:
// TLS verification enabled, redirects disabled.
func NewClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Deliver POSTs JSON to Config.URL with retry, delivery headers, and optional bearer auth.
// It returns nil on 2xx success. Non-HTTPS URLs are rejected as non-retryable.
func Deliver(ctx context.Context, client *http.Client, d Delivery, log logrus.FieldLogger) error {
	cfg := d.Config.WithDefaults()

	parsedURL, err := url.Parse(cfg.URL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" || parsedURL.Hostname() == "" {
		return NonRetryablef("insecure or invalid URL rejected: %s", cfg.URL)
	}

	ownedClient := false
	if client == nil {
		client = NewClient()
		ownedClient = true
	}
	if ownedClient {
		defer client.CloseIdleConnections()
	}

	deadlineCtx, cancel := context.WithTimeout(ctx, cfg.Deadline)
	defer cancel()

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		if err := deadlineCtx.Err(); err != nil {
			return fmt.Errorf("deadline exceeded after %d attempts: %w", attempt-1, err)
		}

		reqCtx, reqCancel := context.WithTimeout(deadlineCtx, cfg.Timeout)
		err = doRequest(reqCtx, client, cfg.URL, d.BearerToken, d.Payload, d.DeliveryID, attempt, log)
		reqCancel()

		if err == nil {
			return nil
		}

		if IsNonRetryable(err) {
			return err
		}

		if attempt < cfg.MaxAttempts {
			backoff := ComputeBackoff(attempt, cfg.BackoffDelay, cfg.MaxBackoff)
			log.Infof("webhook delivery %s: attempt %d/%d failed (%v), retrying in %s", d.DeliveryID, attempt, cfg.MaxAttempts, err, backoff)

			select {
			case <-deadlineCtx.Done():
				return fmt.Errorf("deadline exceeded during backoff after %d attempts: %w", attempt, deadlineCtx.Err())
			case <-time.After(backoff):
			}
		} else {
			return fmt.Errorf("all %d attempts exhausted: %w", cfg.MaxAttempts, err)
		}
	}

	return fmt.Errorf("all %d attempts exhausted", cfg.MaxAttempts)
}

func doRequest(ctx context.Context, client *http.Client, targetURL, bearerToken string, payload []byte, deliveryID string, attempt int, log logrus.FieldLogger) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader(string(payload)))
	if err != nil {
		return NonRetryable(fmt.Errorf("failed to create request: %w", err))
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Flightctl-Delivery-Id", deliveryID)
	req.Header.Set("X-Flightctl-Delivery-Attempt", fmt.Sprintf("%d", attempt))
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBodySize))

	retryable, msg := ClassifyHTTPStatus(resp.StatusCode)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Infof("webhook delivery %s: attempt %d succeeded with HTTP %d", deliveryID, attempt, resp.StatusCode)
		return nil
	}
	if !retryable {
		return NonRetryablef("%s from %s", msg, targetURL)
	}
	return fmt.Errorf("%s from %s", msg, targetURL)
}
