package authprovider_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

const (
	keycloakTestUser = "testuser"
	keycloakTestPass = "testpass"
	loginURLPrefix   = "Opening login URL in default browser: "
	loginURLTimeout  = 30 * time.Second
	chromedpTimeout  = 60 * time.Second
	loginFlowTimeout = 2 * time.Minute // covers URL wait + chromedp + CLI callback
)

var loginURLRe = regexp.MustCompile(`Opening login URL in default browser:\s*(.+)`)

var _ = Describe("Auth provider (Keycloak OIDC)", Label("authprovider"), func() {
	It("logs in via OAuth --web --no-browser with Keycloak and headless browser", Label("88168", "authprovider"), func() {
		harness := e2e.GetWorkerHarness()
		ctx, cancel := context.WithTimeout(harness.GetTestContext(), loginFlowTimeout)
		defer cancel()

		apiEndpoint := harness.ApiEndpoint()
		authURL, done, err := runLoginCLIWithURL(ctx, harness, apiEndpoint)
		Expect(err).ToNot(HaveOccurred())
		Expect(authURL).ToNot(BeEmpty())

		err = fillKeycloakLoginForm(ctx, authURL, keycloakTestUser, keycloakTestPass)
		Expect(err).ToNot(HaveOccurred(), "chromedp Keycloak login form fill failed")

		select {
		case waitErr := <-done:
			Expect(waitErr).ToNot(HaveOccurred(), "CLI login process should exit successfully")
		case <-ctx.Done():
			Fail("CLI login did not complete within timeout")
		}
	})

	It("can call API after Keycloak login", Label("88539", "authprovider"), func() {
		harness := e2e.GetWorkerHarness()
		_, err := harness.RunGetDevices()
		Expect(err).ToNot(HaveOccurred(), "get devices after Keycloak login should succeed")
	})
})

// runLoginCLIWithURL starts `flightctl login ... --web --no-browser` with the given context.
// When ctx is cancelled, the process is killed. Returns the printed auth URL and a channel
// that receives the process exit error when the CLI exits. done is nil when err != nil.
func runLoginCLIWithURL(ctx context.Context, harness *e2e.Harness, apiEndpoint string) (authURL string, done <-chan error, err error) {
	args := []string{
		"login", apiEndpoint,
		"--insecure-skip-tls-verify",
		"--provider", keycloakAuthProviderName,
		"--web", "--no-browser",
	}
	flightctlPath := harness.GetFlightctlPath()
	logrus.Infof("[authprovider] Keycloak login command: %s %s (env: API_ENDPOINT=%s)", flightctlPath, strings.Join(args, " "), apiEndpoint)
	cmd := exec.CommandContext(ctx, flightctlPath, args...)
	cmd.Env = append(os.Environ(), "API_ENDPOINT="+apiEndpoint)

	stdoutPipe, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		return "", nil, pipeErr
	}
	stderrPipe, pipeErr := cmd.StderrPipe()
	if pipeErr != nil {
		return "", nil, pipeErr
	}

	if err = cmd.Start(); err != nil {
		return "", nil, err
	}

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- cmd.Wait()
		close(doneCh)
	}()

	var copyWg sync.WaitGroup
	copyWg.Add(2)
	var stderrBuf bytes.Buffer
	go func() {
		defer copyWg.Done()
		_, _ = io.Copy(&stderrBuf, stderrPipe)
	}()

	var stdoutBuf bytes.Buffer
	urlCh := make(chan string, 1)
	go func() {
		defer copyWg.Done()
		scanner := bufio.NewScanner(io.TeeReader(stdoutPipe, &stdoutBuf))
		for scanner.Scan() {
			line := scanner.Text()
			if match := loginURLRe.FindStringSubmatch(line); len(match) > 1 {
				urlCh <- strings.TrimSpace(match[1])
				return
			}
		}
	}()

	logCommandError := func(what string, err error) {
		logrus.Errorf("[authprovider] %s: %v", what, err)
		if stderrBuf.Len() > 0 {
			logrus.Errorf("[authprovider] login command stderr:\n%s", stderrBuf.String())
		}
		if stdoutBuf.Len() > 0 {
			logrus.Errorf("[authprovider] login command stdout:\n%s", stdoutBuf.String())
		}
	}

	waitPipesThenLog := func(what string, err error) (string, <-chan error, error) {
		pipeDone := make(chan struct{})
		go func() { copyWg.Wait(); close(pipeDone) }()
		select {
		case <-pipeDone:
		case <-time.After(500 * time.Millisecond):
		}
		logCommandError(what, err)
		return "", nil, err
	}

	select {
	case authURL = <-urlCh:
		return authURL, doneCh, nil
	case waitErr := <-doneCh:
		return waitPipesThenLog("login command exited before printing URL", fmt.Errorf("login command exited: %w", waitErr))
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_, _, e := waitPipesThenLog("login command failed (context cancelled)", ctx.Err())
		return "", nil, e
	case <-time.After(loginURLTimeout):
		_ = cmd.Process.Kill()
		_, _, e := waitPipesThenLog("timeout waiting for login URL", fmt.Errorf("timeout waiting for login URL"))
		return "", nil, e
	}
}

// fillKeycloakLoginForm uses chromedp to navigate to the auth URL, fill username/password, and submit.
func fillKeycloakLoginForm(ctx context.Context, authURL, username, password string) error {
	headless := os.Getenv("E2E_HEADED") == ""
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", headless),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()

	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	browserCtx, cancelTimeout := context.WithTimeout(browserCtx, chromedpTimeout)
	defer cancelTimeout()

	logrus.Infof("[authprovider] chromedp navigating to Keycloak login (headless=%v): %s", headless, authURL)
	return chromedp.Run(browserCtx,
		chromedp.Navigate(authURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.WaitVisible(`#username`, chromedp.ByQuery),
		chromedp.SendKeys(`#username`, username, chromedp.ByQuery),
		chromedp.SendKeys(`#password`, password, chromedp.ByQuery),
		chromedp.Click(`#kc-login`, chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			tick := time.NewTicker(100 * time.Millisecond)
			defer tick.Stop()
			for {
				var loc string
				if err := chromedp.Location(&loc).Do(ctx); err != nil {
					return err
				}
				if strings.Contains(loc, "/callback") {
					return nil
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-tick.C:
				}
			}
		}),
	)
}
