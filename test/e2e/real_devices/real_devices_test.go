package real_devices_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"

	"github.com/chromedp/chromedp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const (
	enrollTimeout     = 5 * time.Minute
	enrollPolling     = 10 * time.Second
	onlineTimeout     = 5 * time.Minute
	onlinePolling     = 10 * time.Second
	appTimeout        = 10 * time.Minute
	appPolling        = 15 * time.Second
	chromedpTimeout   = 60 * time.Second
	httpClientTimeout = 30 * time.Second
)

func deviceAppURL(path string) string {
	host := os.Getenv("REAL_DEVICE_HOST")
	port := os.Getenv("REAL_DEVICE_APP_PORT")
	if port == "" {
		port = "8080"
	}
	return fmt.Sprintf("http://%s:%s%s", host, port, path)
}

func newChromedpContext(parent context.Context) (context.Context, context.CancelFunc) {
	headless := os.Getenv("E2E_HEADED") == ""
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", headless),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("ignore-certificate-errors", true),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(parent, opts...)

	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)

	browserCtx, cancelTimeout := context.WithTimeout(browserCtx, chromedpTimeout)

	cancel := func() {
		cancelTimeout()
		cancelBrowser()
		cancelAlloc()
	}
	return browserCtx, cancel
}

func fleetLabelsFromEnv() map[string]string {
	fleetYAMLPath := os.Getenv("REAL_DEVICE_FLEET_YAML")
	if fleetYAMLPath == "" {
		return nil
	}

	data, err := os.ReadFile(fleetYAMLPath)
	if err != nil {
		logrus.Warnf("Could not read fleet YAML for label extraction: %v", err)
		return nil
	}

	var fleet struct {
		Spec struct {
			Selector struct {
				MatchLabels map[string]string `yaml:"matchLabels"`
			} `yaml:"selector"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &fleet); err != nil {
		logrus.Warnf("Could not parse fleet YAML for label extraction: %v", err)
		return nil
	}

	return fleet.Spec.Selector.MatchLabels
}

var _ = Describe("Real device deployment", Label("real-devices"), Ordered, func() {
	var (
		harness  *e2e.Harness
		deviceID string
		appName  string
		appPort  string
	)

	BeforeAll(func() {
		harness = e2e.GetWorkerHarness()
		appName = os.Getenv("REAL_DEVICE_APP_NAME")
		Expect(appName).ToNot(BeEmpty(), "REAL_DEVICE_APP_NAME environment variable is required")
		appPort = os.Getenv("REAL_DEVICE_APP_PORT")
		Expect(appPort).ToNot(BeEmpty(), "REAL_DEVICE_APP_PORT environment variable is required")
		_, err := strconv.Atoi(appPort)
		Expect(err).ToNot(HaveOccurred(), "REAL_DEVICE_APP_PORT must be a valid integer")
	})

	It("enrolls the device and deploys application via fleet", func() {
		presetID := os.Getenv("REAL_DEVICE_ID")
		if presetID != "" {
			deviceID = presetID
			logrus.Infof("Using pre-enrolled device: %s", deviceID)

			fleetLabels := fleetLabelsFromEnv()
			if len(fleetLabels) > 0 {
				err := harness.SetLabelsForDevice(deviceID, fleetLabels)
				Expect(err).ToNot(HaveOccurred(), "failed to set fleet labels on device")
			}
		} else {
			var enrollmentID string
			Eventually(func() error {
				id, err := device.GetEnrollmentID()
				if err != nil {
					return err
				}
				enrollmentID = id
				return nil
			}).WithTimeout(enrollTimeout).WithPolling(enrollPolling).Should(Succeed(), "enrollment ID should appear in agent logs")

			logrus.Infof("Found enrollment ID: %s", enrollmentID)

			enrollmentRequest := harness.WaitForEnrollmentRequest(enrollmentID)
			Expect(enrollmentRequest).ToNot(BeNil(), "enrollment request must exist before approval")

			fleetLabels := fleetLabelsFromEnv()
			approval := harness.TestEnrollmentApproval(fleetLabels)
			harness.ApproveEnrollment(enrollmentID, approval)

			deviceID = enrollmentID
		}

		Eventually(func() (v1beta1.DeviceSummaryStatusType, error) {
			return harness.GetDeviceWithStatusSummary(deviceID)
		}).WithTimeout(onlineTimeout).WithPolling(onlinePolling).Should(Equal(v1beta1.DeviceSummaryStatusOnline), "device should come online")

		logrus.Infof("Device %s is online", deviceID)

		err := harness.WaitForApplicationStatus(deviceID, appName, v1beta1.ApplicationStatusRunning, appTimeout, appPolling)
		Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("application %s should be running", appName))

		logrus.Infof("Application %s is running on device %s", appName, deviceID)
	})

	It("application health endpoint responds", func() {
		Expect(deviceID).ToNot(BeEmpty(), "device must be enrolled first")

		healthURL := deviceAppURL("/api/health")
		client := &http.Client{Timeout: httpClientTimeout}

		Eventually(func() error {
			resp, err := client.Get(healthURL) // #nosec G107 - test code with controlled URL
			if err != nil {
				return fmt.Errorf("GET %s: %w", healthURL, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("GET %s returned %d: %s", healthURL, resp.StatusCode, string(body))
			}
			return nil
		}).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(Succeed(), "health endpoint should return 200")
	})

	It("application web UI is accessible", func() {
		Expect(deviceID).ToNot(BeEmpty(), "device must be enrolled first")

		ctx := harness.GetTestContext()
		browserCtx, cancel := newChromedpContext(ctx)
		defer cancel()

		appURL := deviceAppURL("/")
		logrus.Infof("Navigating to application UI: %s", appURL)

		var title string
		err := chromedp.Run(browserCtx,
			chromedp.Navigate(appURL),
			chromedp.WaitReady("body", chromedp.ByQuery),
			chromedp.Title(&title),
		)
		Expect(err).ToNot(HaveOccurred(), "chromedp should navigate to application UI")
		Expect(title).ToNot(BeEmpty(), "page title should not be empty")
		logrus.Infof("Application UI title: %s", title)
	})

	It("application responds to user interaction", func() {
		Expect(deviceID).ToNot(BeEmpty(), "device must be enrolled first")

		ctx := harness.GetTestContext()
		browserCtx, cancel := newChromedpContext(ctx)
		defer cancel()

		appURL := deviceAppURL("/")
		inputSel := os.Getenv("REAL_DEVICE_INPUT_SELECTOR")
		if inputSel == "" {
			inputSel = "input, textarea"
		}
		submitSel := os.Getenv("REAL_DEVICE_SUBMIT_SELECTOR")
		if submitSel == "" {
			submitSel = "button[type=submit], form button"
		}
		responseSel := os.Getenv("REAL_DEVICE_RESPONSE_SELECTOR")
		if responseSel == "" {
			responseSel = ".response, .message, .output, .chat-message, [data-testid=response]"
		}

		var responseText string
		err := chromedp.Run(browserCtx,
			chromedp.Navigate(appURL),
			chromedp.WaitReady("body", chromedp.ByQuery),
			chromedp.WaitVisible(inputSel, chromedp.ByQuery),
			chromedp.SendKeys(inputSel, "Hello", chromedp.ByQuery),
			chromedp.Click(submitSel, chromedp.ByQuery),
			chromedp.Sleep(5*time.Second),
			chromedp.WaitVisible(responseSel, chromedp.ByQuery),
			chromedp.Text(responseSel, &responseText, chromedp.ByQuery),
		)
		Expect(err).ToNot(HaveOccurred(), "chromedp should interact with the application")
		Expect(responseText).ToNot(BeEmpty(), "application should produce a response")
		Expect(strings.ToLower(responseText)).ToNot(ContainSubstring("error"), "response should not contain error")
		logrus.Infof("Application response: %s", responseText)
	})

	It("application streams responses via SSE", func() {
		Expect(deviceID).ToNot(BeEmpty(), "device must be enrolled first")

		streamEndpoint := os.Getenv("REAL_DEVICE_STREAM_ENDPOINT")
		if streamEndpoint == "" {
			streamEndpoint = "/api/chat/stream"
		}
		streamURL := deviceAppURL(streamEndpoint)

		requestBody := os.Getenv("REAL_DEVICE_STREAM_BODY")
		if requestBody == "" {
			requestBody = `{"message":"Hello"}`
		}

		client := &http.Client{Timeout: 2 * time.Minute}
		req, err := http.NewRequestWithContext(harness.GetTestContext(), http.MethodPost, streamURL, strings.NewReader(requestBody))
		Expect(err).ToNot(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("POST %s should succeed", streamURL))
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK), fmt.Sprintf("POST %s should return 200", streamURL))

		body, err := io.ReadAll(resp.Body)
		Expect(err).ToNot(HaveOccurred())
		bodyStr := string(body)

		dataEvents := strings.Count(bodyStr, "data:")
		Expect(dataEvents).To(BeNumerically(">=", 2), "SSE stream should contain at least 2 data events")
		Expect(bodyStr).To(ContainSubstring("[DONE]"), "SSE stream should end with [DONE]")
		logrus.Infof("SSE stream received %d data events", dataEvents)
	})

	It("handles concurrent users", func() {
		Expect(deviceID).ToNot(BeEmpty(), "device must be enrolled first")

		streamEndpoint := os.Getenv("REAL_DEVICE_STREAM_ENDPOINT")
		if streamEndpoint == "" {
			streamEndpoint = "/api/chat/stream"
		}
		streamURL := deviceAppURL(streamEndpoint)

		numUsers := 5
		if n := os.Getenv("REAL_DEVICE_CONCURRENT_USERS"); n != "" {
			parsed, err := strconv.Atoi(n)
			Expect(err).ToNot(HaveOccurred(), "REAL_DEVICE_CONCURRENT_USERS must be a valid integer")
			numUsers = parsed
		}

		messages := []string{
			`{"message":"What is the weather like?"}`,
			`{"message":"Tell me a story"}`,
			`{"message":"How does this work?"}`,
			`{"message":"Explain something simple"}`,
			`{"message":"Hello there"}`,
		}
		if body := os.Getenv("REAL_DEVICE_STREAM_BODY"); body != "" {
			messages = []string{body}
		}

		logrus.Infof("Launching %d concurrent users against %s", numUsers, streamURL)

		type result struct {
			userID     int
			statusCode int
			dataEvents int
			err        error
			duration   time.Duration
		}

		results := make([]result, numUsers)
		var wg sync.WaitGroup
		wg.Add(numUsers)

		for i := 0; i < numUsers; i++ {
			go func(userID int) {
				defer wg.Done()
				defer GinkgoRecover()

				start := time.Now()
				body := messages[userID%len(messages)]
				client := &http.Client{Timeout: 3 * time.Minute}
				req, err := http.NewRequestWithContext(harness.GetTestContext(), http.MethodPost, streamURL, strings.NewReader(body))
				if err != nil {
					results[userID] = result{userID: userID, err: err}
					return
				}
				req.Header.Set("Content-Type", "application/json")

				resp, err := client.Do(req)
				if err != nil {
					results[userID] = result{userID: userID, err: fmt.Errorf("request failed: %w", err), duration: time.Since(start)}
					return
				}
				defer resp.Body.Close()

				respBody, err := io.ReadAll(resp.Body)
				if err != nil {
					results[userID] = result{userID: userID, err: fmt.Errorf("reading body: %w", err), statusCode: resp.StatusCode, duration: time.Since(start)}
					return
				}

				results[userID] = result{
					userID:     userID,
					statusCode: resp.StatusCode,
					dataEvents: strings.Count(string(respBody), "data:"),
					duration:   time.Since(start),
				}
			}(i)
		}

		wg.Wait()

		var failures []string
		for _, r := range results {
			if r.err != nil {
				failures = append(failures, fmt.Sprintf("user %d: %v", r.userID, r.err))
				continue
			}
			if r.statusCode != http.StatusOK {
				failures = append(failures, fmt.Sprintf("user %d: HTTP %d", r.userID, r.statusCode))
				continue
			}
			if r.dataEvents < 2 {
				failures = append(failures, fmt.Sprintf("user %d: only %d data events", r.userID, r.dataEvents))
				continue
			}
			logrus.Infof("User %d: %d data events in %s", r.userID, r.dataEvents, r.duration)
		}

		Expect(failures).To(BeEmpty(), fmt.Sprintf("%d/%d concurrent users failed:\n%s", len(failures), numUsers, strings.Join(failures, "\n")))
		logrus.Infof("All %d concurrent users received valid responses", numUsers)
	})
})
