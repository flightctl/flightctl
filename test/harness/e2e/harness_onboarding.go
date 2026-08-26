package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

const (
	cockpitPort       = 9090
	cockpitIframeName = "cockpit1:localhost/system-onboarding"
)

// Wizard step IDs match the React source in cockpit-onboarding.
const (
	WizardStepNetwork         = "networkStep"
	WizardStepNetworkServices = "networkServicesStep"
	WizardStepEnrollment      = "enrollmentStep"
	WizardStepLabels          = "labelsStep"
	WizardStepReview          = "reviewStep"
	WizardStepProgress        = "progressStep"
)

// OnboardingBrowser holds a chromedp context used to drive the Cockpit wizard.
type OnboardingBrowser struct {
	allocCtx    context.Context
	allocCancel context.CancelFunc
	ctx         context.Context
	cancel      context.CancelFunc
}

// NewOnboardingBrowser creates a headless Chrome session for driving the wizard.
// The caller must call Close() when done.
func NewOnboardingBrowser(parent context.Context) (*OnboardingBrowser, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("ignore-certificate-errors", true),
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-gpu", true),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(parent, opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)

	// Run a no-op action to actually start Chrome now. Without this the allocation
	// is lazy and a missing/broken Chrome binary would only surface later as an
	// opaque CockpitLogin timeout; here it fails with a clear browser-startup error.
	if err := chromedp.Run(ctx); err != nil {
		cancel()
		allocCancel()
		return nil, fmt.Errorf("starting headless Chrome: %w", err)
	}

	return &OnboardingBrowser{
		allocCtx:    allocCtx,
		allocCancel: allocCancel,
		ctx:         ctx,
		cancel:      cancel,
	}, nil
}

func (b *OnboardingBrowser) Close() {
	b.cancel()
	b.allocCancel()
}

// Context returns the chromedp browsing context.
func (b *OnboardingBrowser) Context() context.Context {
	return b.ctx
}

// Screenshot captures a full-page PNG of the current browser state (the login
// page or the Cockpit page hosting the wizard iframe). It is intended for
// failure diagnostics; callers on the failure path should treat any error as
// best-effort and not fatal.
func (b *OnboardingBrowser) Screenshot() ([]byte, error) {
	var buf []byte
	if err := chromedp.Run(b.ctx, chromedp.FullScreenshot(&buf, 90)); err != nil {
		return nil, fmt.Errorf("capturing screenshot: %w", err)
	}
	return buf, nil
}

// CockpitLogin navigates to the Cockpit UI, logs in, and waits for the
// onboarding plugin to load. cockpitAddr is host:port (e.g. "127.0.0.1:19090"
// when using an SSH tunnel, or "192.168.122.10:9090" for direct access).
func (b *OnboardingBrowser) CockpitLogin(cockpitAddr, user, password string) error {
	url := fmt.Sprintf("https://%s/system-onboarding", cockpitAddr)
	return chromedp.Run(b.ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`#login-user-input`, chromedp.ByID),
		chromedp.SetValue(`#login-user-input`, user, chromedp.ByID),
		chromedp.SetValue(`#login-password-input`, password, chromedp.ByID),
		chromedp.Click(`#login-button`, chromedp.ByID),
		// Wait for the onboarding iframe to appear.
		chromedp.WaitVisible(
			fmt.Sprintf(`iframe[name='%s']`, cockpitIframeName),
			chromedp.ByQuery,
		),
		// Wait until the plugin settles: on a fresh device the wizard renders;
		// once onboarding has completed the plugin shows the already-complete
		// screen instead, and the wizard never appears. Accept either so login
		// works in both states (TC11 re-opens the plugin after completion).
		b.iframeWaitVisibleAny(
			[]string{`#system-onboarding-wizard`, `#system-onboarding-already-complete`},
			30*time.Second,
		),
	)
}

// WizardSelectNIC selects the first available NIC radio button in the network
// interface table.
func (b *OnboardingBrowser) WizardSelectNIC() error {
	return chromedp.Run(b.ctx,
		b.iframeWaitVisible(`table[aria-label='Network interface selector']`, 10*time.Second),
		b.iframeClick(`table[aria-label='Network interface selector'] tbody tr:first-child td:first-child input[type='radio']`),
	)
}

// WizardConfigureStaticIPv4 switches to manual IPv4 and fills address fields.
func (b *OnboardingBrowser) WizardConfigureStaticIPv4(ip, mask, gateway, dns string) error {
	steps := []chromedp.Action{
		b.iframeClick(`#static-ip-radio`),
		b.iframeWaitVisible(`#ipv4-address`, 5*time.Second),
		b.iframeClearAndType(`#ipv4-address`, ip),
		b.iframeClearAndType(`#subnet-mask`, mask),
		b.iframeClearAndType(`#gateway-ip`, gateway),
	}
	if dns != "" {
		steps = append(steps,
			b.iframeClick(`#manual-dns-ipv4-radio`),
			b.iframeWaitVisible(`#primary-dns-ipv4`, 5*time.Second),
			b.iframeClearAndType(`#primary-dns-ipv4`, dns),
		)
	}
	return chromedp.Run(b.ctx, steps...)
}

// WizardConfigureNTP enables the custom NTP toggle and enters the server hostname.
func (b *OnboardingBrowser) WizardConfigureNTP(server string) error {
	return chromedp.Run(b.ctx,
		b.iframeEnsureSwitchOn(`ntp-servers`),
		b.iframeWaitVisible(`#ntp-server-input`, 5*time.Second),
		b.iframeClearAndType(`#ntp-server-input`, server),
	)
}

// WizardConfigureProxy enables the proxy toggle and fills the proxy fields.
func (b *OnboardingBrowser) WizardConfigureProxy(host, port, user, pass string) error {
	steps := []chromedp.Action{
		b.iframeEnsureSwitchOn(`proxy-enabled`),
		b.iframeWaitVisible(`#proxy-hostname-input`, 5*time.Second),
		b.iframeClearAndType(`#proxy-hostname-input`, host),
		b.iframeClearAndType(`#proxy-port-input`, port),
	}
	if user != "" {
		steps = append(steps, b.iframeClearAndType(`#proxy-username-input`, user))
	}
	if pass != "" {
		steps = append(steps, b.iframeClearAndType(`#proxy-password-input`, pass))
	}
	return chromedp.Run(b.ctx, steps...)
}

// WizardConfigureEnrollment enables enrollment and fills endpoint + token.
func (b *OnboardingBrowser) WizardConfigureEnrollment(endpoint, token string) error {
	return chromedp.Run(b.ctx,
		b.iframeEnsureSwitchOn(`flightctl-enrollment`),
		b.iframeClick(`#configure-new-flightctl`),
		b.iframeClick(`#auth-token`),
		b.iframeWaitVisible(`#credential-token`, 5*time.Second),
		b.iframeClearAndType(`#credential-token`, token),
		b.iframeWaitVisible(`#endpoint-flightctl`, 5*time.Second),
		b.iframeClearAndType(`#endpoint-flightctl`, endpoint),
	)
}

// WizardDisableEnrollment turns the Flight Control enrollment switch off. The
// wizard defaults enrollment.selected to true, and the Review step's validation
// bounces Apply back to the Enrollment step unless a server address is supplied.
// Tests that do not enroll must call this while on the Enrollment step so Apply
// can proceed.
func (b *OnboardingBrowser) WizardDisableEnrollment() error {
	return chromedp.Run(b.ctx,
		b.iframeWaitVisible(`#flightctl-enrollment`, 5*time.Second),
		b.iframeEnsureSwitchOff(`flightctl-enrollment`),
	)
}

// WizardEnableEnrollment turns the Flight Control enrollment switch on WITHOUT
// forcing the "Request new" credential-provisioning path. When the device already
// has an agent config carrying a client certificate (detected by
// read-flightctl-config.sh), the wizard auto-selects "Use existing" and hides the
// credential fields. Specs that need to observe that auto-detection must use this
// helper rather than WizardConfigureEnrollment, which clicks
// #configure-new-flightctl and would override the detected state.
func (b *OnboardingBrowser) WizardEnableEnrollment() error {
	return chromedp.Run(b.ctx,
		b.iframeWaitVisible(`#flightctl-enrollment`, 5*time.Second),
		b.iframeEnsureSwitchOn(`flightctl-enrollment`),
	)
}

// WizardSetTLSInsecure turns off the enrollment step's "Verify TLS certificates"
// switch (#tls-verification), which sets the wizard's tlsMode to "insecure" so the
// generated `flightctl login` runs with -k. Test deployments present a self-signed
// certificate the VM does not trust, so enrollment specs must disable verification
// (or supply a custom CA) for the login inside flightctl-enroll.sh to succeed. Call
// it after WizardConfigureEnrollment, which selects the "Request new" body where
// this switch lives.
func (b *OnboardingBrowser) WizardSetTLSInsecure() error {
	return chromedp.Run(b.ctx,
		b.iframeWaitVisible(`#tls-verification`, 5*time.Second),
		b.iframeEnsureSwitchOff(`tls-verification`),
	)
}

// WizardEnrollmentUsesExisting reports whether the enrollment step's "Use existing"
// radio (#use-existing-flightctl) is selected. The wizard enables and auto-selects
// it only when it detects a pre-provisioned agent config carrying a client
// certificate.
func (b *OnboardingBrowser) WizardEnrollmentUsesExisting() (bool, error) {
	var checked bool
	js := fmt.Sprintf(`
		(function() {
			var doc = %s;
			if (!doc) return false;
			var el = doc.querySelector('#use-existing-flightctl');
			return !!(el && el.checked);
		})()
	`, b.iframeDoc())
	err := chromedp.Run(b.ctx, chromedp.Evaluate(js, &checked))
	return checked, err
}

// WizardEnrollmentCredentialFieldVisible reports whether the enrollment step's
// credential-token field (#credential-token) is present and visible. It is used to
// assert that the "Use existing" path hides the credential-provisioning UI.
func (b *OnboardingBrowser) WizardEnrollmentCredentialFieldVisible() (bool, error) {
	var visible bool
	js := fmt.Sprintf(`
		(function() {
			var doc = %s;
			if (!doc) return false;
			var el = doc.querySelector('#credential-token');
			if (!el) return false;
			var rect = el.getBoundingClientRect();
			return rect.width > 0 && rect.height > 0;
		})()
	`, b.iframeDoc())
	err := chromedp.Run(b.ctx, chromedp.Evaluate(js, &visible))
	return visible, err
}

// WizardSetHostname sets the hostname on the Labels page.
func (b *OnboardingBrowser) WizardSetHostname(hostname string) error {
	return chromedp.Run(b.ctx,
		b.iframeWaitVisible(`#hostname-input`, 5*time.Second),
		b.iframeClearAndType(`#hostname-input`, hostname),
	)
}

// WizardAddLabel adds a custom label key/value pair. labelIndex is 0-based;
// the first row already exists. For subsequent rows, "Add another label" is clicked.
func (b *OnboardingBrowser) WizardAddLabel(labelIndex int, key, value string) error {
	if labelIndex > 0 {
		if err := chromedp.Run(b.ctx,
			b.iframeClickXPath(`//button[contains(., 'Add another label')]`),
		); err != nil {
			return fmt.Errorf("clicking 'Add another label': %w", err)
		}
	}
	keySel := fmt.Sprintf(`#device-label-key-%d`, labelIndex)
	valSel := fmt.Sprintf(`#device-label-value-%d`, labelIndex)
	return chromedp.Run(b.ctx,
		b.iframeWaitVisible(keySel, 5*time.Second),
		b.iframeClearAndType(keySel, key),
		b.iframeClearAndType(valSel, value),
	)
}

// WizardSetHostnameAndLabels sets the hostname and adds all provided labels.
func (b *OnboardingBrowser) WizardSetHostnameAndLabels(hostname string, labels map[string]string) error {
	if err := b.WizardSetHostname(hostname); err != nil {
		return err
	}
	i := 0
	for k, v := range labels {
		if err := b.WizardAddLabel(i, k, v); err != nil {
			return fmt.Errorf("adding label %q=%q: %w", k, v, err)
		}
		i++
	}
	return nil
}

// currentWizardStep returns the label of the wizard's current nav step (the
// text of .pf-v6-c-wizard__nav-link.pf-m-current), or "" when it cannot be
// determined yet.
func (b *OnboardingBrowser) currentWizardStep() (string, error) {
	js := fmt.Sprintf(`
		(function() {
			var doc = %s;
			if (!doc) return '';
			var cur = doc.querySelector('.pf-v6-c-wizard__nav-link.pf-m-current');
			return cur ? cur.innerText.trim() : '';
		})()
	`, b.iframeDoc())
	var step string
	err := chromedp.Run(b.ctx, chromedp.Evaluate(js, &step))
	return step, err
}

// clickFooterAndAwaitStepChange clicks the footer's primary ("Next") or
// secondary ("Back") button and then waits until the wizard's current nav step
// actually changes, rather than blindly sleeping. A blind sleep returns success
// even when a slow React render has not advanced the step yet, so a later helper
// fails several steps downstream with an opaque "element not found" error. If the
// step does not change within the timeout, fail here with the current wizard
// state so the real cause is visible.
func (b *OnboardingBrowser) clickFooterAndAwaitStepChange(variant string) error {
	before, err := b.currentWizardStep()
	if err != nil {
		return err
	}
	if err := chromedp.Run(b.ctx, b.iframeClickFooterButton(variant)); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := b.currentWizardStep()
		if err != nil {
			return err
		}
		if cur != "" && cur != before {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("wizard step did not change from %q after clicking %s button; state: %s",
		before, variant, b.WizardDebugState())
}

// WizardClickNext clicks the primary ("Next") button in the wizard footer and
// waits for the wizard to advance to the next step.
func (b *OnboardingBrowser) WizardClickNext() error {
	return b.clickFooterAndAwaitStepChange("primary")
}

// WizardSetConnectivityHost fills the Review step's "Connectivity test host"
// field (#review-connectivity-test-host). The Review step is invalid — and Apply
// refuses to advance to the progress page — unless this field holds a valid
// hostname or IP.
//
// NOTE: a reachable host is NOT enough on its own. check-connectivity.sh treats
// an unreachable host (ping AND TCP-connect to the port both fail) as FATAL when
// the connectivity test is marked required (the default), rolling back the whole
// apply. For tests that don't need real connectivity (hostname/labels/NM/proxy/
// NTP without enrollment), also call WizardSetConnectivityRequired(false) so an
// unreachable host degrades to a non-fatal warning. Use required=true with an
// unreachable/unresolvable host to deliberately force an apply failure (TC3).
func (b *OnboardingBrowser) WizardSetConnectivityHost(host string) error {
	if err := chromedp.Run(b.ctx,
		b.iframeWaitVisible(`#review-connectivity-test-host`, 8*time.Second),
	); err != nil {
		return err
	}
	// The field is a React-controlled input: verify the typed value is reflected
	// back (i.e. React's onChange fired and updated the model) before proceeding,
	// retrying a few times. If it never sticks, the Review step stays invalid and
	// Apply silently refuses to advance — fail here with a clear message instead.
	readValueJS := fmt.Sprintf(`
		(function() {
			var doc = %s;
			if (!doc) return 'NODOC';
			var el = doc.querySelector('#review-connectivity-test-host');
			return el ? String(el.value) : 'NOEL';
		})()
	`, b.iframeDoc())
	var got string
	for i := 0; i < 10; i++ {
		if err := chromedp.Run(b.ctx, b.iframeClearAndType(`#review-connectivity-test-host`, host)); err != nil {
			return err
		}
		time.Sleep(250 * time.Millisecond)
		if err := chromedp.Run(b.ctx, chromedp.Evaluate(readValueJS, &got)); err != nil {
			return err
		}
		if got == host {
			return nil
		}
	}
	return fmt.Errorf("WizardSetConnectivityHost: value did not stick after retries (got %q, want %q)", got, host)
}

// WizardSetConnectivityRequired toggles the Review step's "Required" switch for
// the connectivity test (#review-connectivity-test-required). When off, an
// unreachable connectivity host degrades to a non-fatal warning instead of
// rolling back the apply — required for tests that only configure the device
// (hostname/labels/network/proxy/NTP) without real outbound connectivity.
// It verifies the switch's checked state matches, retrying a few times, since
// the toggle is React-controlled.
func (b *OnboardingBrowser) WizardSetConnectivityRequired(required bool) error {
	const sel = `#review-connectivity-test-required`
	if err := chromedp.Run(b.ctx, b.iframeWaitVisible(sel, 8*time.Second)); err != nil {
		return err
	}
	readCheckedJS := fmt.Sprintf(`
		(function() {
			var doc = %s;
			if (!doc) return 'NODOC';
			var el = doc.querySelector('%s');
			if (!el) return 'NOEL';
			return el.checked ? 'true' : 'false';
		})()
	`, b.iframeDoc(), sel)
	want := "false"
	if required {
		want = "true"
	}
	var got string
	for i := 0; i < 10; i++ {
		if err := chromedp.Run(b.ctx, chromedp.Evaluate(readCheckedJS, &got)); err != nil {
			return err
		}
		if got == want {
			return nil
		}
		toggle := b.iframeEnsureSwitchOn(strings.TrimPrefix(sel, "#"))
		if !required {
			toggle = b.iframeEnsureSwitchOff(strings.TrimPrefix(sel, "#"))
		}
		if err := chromedp.Run(b.ctx, toggle); err != nil {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("WizardSetConnectivityRequired: checked state did not settle (got %q, want %q)", got, want)
}

// WizardDebugState returns a snapshot of wizard state useful for diagnosing why
// an apply did not progress: the current step name, whether the primary
// (Next/Apply) button is disabled, the connectivity-test host input value, and
// whether success/danger alerts are present.
func (b *OnboardingBrowser) WizardDebugState() string {
	js := fmt.Sprintf(`
		(function() {
			var doc = %s;
			if (!doc) return 'no iframe doc';
			var cur = doc.querySelector('.pf-v6-c-wizard__nav-link.pf-m-current');
			var step = cur ? cur.innerText.trim() : '(none)';
			var next = doc.querySelector('#wizard-next-btn');
			var nextState = next ? ('disabled=' + !!next.disabled + ' aria-disabled=' + next.getAttribute('aria-disabled') + ' text=' + JSON.stringify(next.innerText.trim())) : '(no #wizard-next-btn)';
			var conn = doc.querySelector('#review-connectivity-test-host');
			var connVal = conn ? JSON.stringify(conn.value) : '(no connectivity input)';
			var success = !!doc.querySelector('.pf-v6-c-alert.pf-m-success');
			var danger = !!doc.querySelector('.pf-v6-c-alert.pf-m-danger');
			return 'currentStep=' + JSON.stringify(step) + ' | nextBtn: ' + nextState + ' | connHost=' + connVal + ' | successAlert=' + success + ' | dangerAlert=' + danger;
		})()
	`, b.iframeDoc())
	var out string
	if err := chromedp.Run(b.ctx, chromedp.Evaluate(js, &out)); err != nil {
		return fmt.Sprintf("WizardDebugState error: %v", err)
	}
	return out
}

// WizardClickApply clicks the Review step's primary button ("Apply" or "Enroll").
// The button authorizes the apply on mousedown (isApplyAuthorized), so the click
// helper must dispatch a full mousedown/mouseup/click sequence — a bare
// element.click() would navigate to the progress step without ever authorizing
// (and running) the enrollment, leaving the progress page idle forever.
func (b *OnboardingBrowser) WizardClickApply() error {
	return chromedp.Run(b.ctx, b.iframeClickFooterButton("primary"))
}

// WizardClickBack clicks the secondary ("Back") button in the wizard footer and
// waits for the wizard to return to the previous step.
func (b *OnboardingBrowser) WizardClickBack() error {
	return b.clickFooterAndAwaitStepChange("secondary")
}

// WizardWaitForCompletion waits until the progress page reaches a terminal state.
// It races the success alert against the danger (failure) alert so a failed apply
// returns promptly instead of blocking for the full timeout. On failure (or on
// timeout) it captures the progress page text — including the failing step's action
// details — so the actual apply error is visible in the test output rather than a
// bare "timed out" message.
func (b *OnboardingBrowser) WizardWaitForCompletion(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	js := fmt.Sprintf(`
		(function() {
			var doc = %s;
			if (!doc) return '';
			if (doc.querySelector('.pf-v6-c-alert.pf-m-success')) return 'success';
			if (doc.querySelector('.pf-v6-c-alert.pf-m-danger')) return 'failed';
			return '';
		})()
	`, b.iframeDoc())
	for {
		var state string
		if err := chromedp.Run(b.ctx, chromedp.Evaluate(js, &state)); err != nil {
			return fmt.Errorf("WizardWaitForCompletion: %w", err)
		}
		switch state {
		case "success":
			return nil
		case "failed":
			// Best-effort diagnostics: we are already returning an error, so a
			// failure to scrape the progress-page text just yields an empty txt.
			txt, _ := b.WizardGetReviewText()
			return fmt.Errorf("wizard apply failed; state: %s\nprogress page:\n%s", b.WizardDebugState(), txt)
		}
		if time.Now().After(deadline) {
			// Best-effort diagnostics: we are already returning an error, so a
			// failure to scrape the progress-page text just yields an empty txt.
			txt, _ := b.WizardGetReviewText()
			return fmt.Errorf("WizardWaitForCompletion: no success/failure alert after %s; state: %s\nprogress page:\n%s", timeout, b.WizardDebugState(), txt)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// WizardWaitForFailure waits until the progress page shows a danger alert.
// It polls (rather than a bare iframeWaitVisible) so that on timeout it can
// embed a full wizard snapshot — current step, primary-button state, and the
// progress-page text — into the returned error. That diagnostic is captured in
// the error itself (and therefore in JUnit's system-err), independent of any
// deferred screenshot/diagnostics hook, which is essential because a plain Go
// defer sees CurrentSpecReport().Failed()==false during the failure unwind.
// If the apply unexpectedly SUCCEEDS, that is reported too so a mis-triggered
// failure scenario is distinguishable from one that never applied at all.
func (b *OnboardingBrowser) WizardWaitForFailure(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	js := fmt.Sprintf(`
		(function() {
			var doc = %s;
			if (!doc) return '';
			if (doc.querySelector('.pf-v6-c-alert.pf-m-danger')) return 'failed';
			if (doc.querySelector('.pf-v6-c-alert.pf-m-success')) return 'success';
			return '';
		})()
	`, b.iframeDoc())
	for {
		var state string
		if err := chromedp.Run(b.ctx, chromedp.Evaluate(js, &state)); err != nil {
			return fmt.Errorf("WizardWaitForFailure: %w", err)
		}
		switch state {
		case "failed":
			return nil
		case "success":
			// Best-effort diagnostics: the apply succeeded when a danger alert
			// was expected, so surface the state to explain the mismatch.
			txt, _ := b.WizardGetReviewText()
			return fmt.Errorf("WizardWaitForFailure: apply SUCCEEDED but a failure (danger alert) was expected; state: %s\nprogress page:\n%s", b.WizardDebugState(), txt)
		}
		if time.Now().After(deadline) {
			// Best-effort diagnostics: no danger alert appeared, so capture the
			// wizard state to reveal whether apply even reached the progress step.
			txt, _ := b.WizardGetReviewText()
			return fmt.Errorf("WizardWaitForFailure: no danger alert after %s; state: %s\nprogress page:\n%s", timeout, b.WizardDebugState(), txt)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// WizardGetReviewText returns the text content of the review page from the iframe.
func (b *OnboardingBrowser) WizardGetReviewText() (string, error) {
	var text string
	err := chromedp.Run(b.ctx,
		chromedp.Evaluate(fmt.Sprintf(`
			(function() {
				var iframe = document.querySelector("iframe[name='%s']");
				if (!iframe || !iframe.contentDocument) return '';
				var el = iframe.contentDocument.querySelector('#system-onboarding-wizard');
				return el ? el.innerText : '';
			})()
		`, cockpitIframeName), &text),
	)
	return text, err
}

// WizardIsAlreadyComplete returns true if the wizard shows the "already complete" message.
func (b *OnboardingBrowser) WizardIsAlreadyComplete() (bool, error) {
	var found bool
	err := chromedp.Run(b.ctx,
		chromedp.Evaluate(fmt.Sprintf(`
			(function() {
				var iframe = document.querySelector("iframe[name='%s']");
				if (!iframe || !iframe.contentDocument) return false;
				return iframe.contentDocument.querySelector('#system-onboarding-already-complete') !== null;
			})()
		`, cockpitIframeName), &found),
	)
	return found, err
}

// WizardNavigateToStep clicks Next repeatedly until the target step is reached.
func (b *OnboardingBrowser) WizardNavigateToStep(targetStepID string) error {
	stepOrder := []string{
		WizardStepNetwork,
		WizardStepNetworkServices,
		WizardStepEnrollment,
		WizardStepLabels,
		WizardStepReview,
	}
	for _, sid := range stepOrder {
		if sid == targetStepID {
			return nil
		}
		if err := b.WizardClickNext(); err != nil {
			return fmt.Errorf("clicking Next to advance past %s toward %s: %w", sid, targetStepID, err)
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("target step %s not in wizard sequence", targetStepID)
}

// WizardConfigureVLAN enables the VLAN toggle and sets the VLAN ID. The toggle
// switch has fieldId "vlan-id"; the required VLAN ID text field is a distinct
// element, "#vlan-id-input". Typing into the wrong element leaves the required
// VLAN ID empty, which keeps the Network step invalid so the wizard never
// advances to Review (Next/Apply stays disabled).
func (b *OnboardingBrowser) WizardConfigureVLAN(vlanID string) error {
	return chromedp.Run(b.ctx,
		b.iframeEnsureSwitchOn(`vlan-id`),
		b.iframeWaitVisible(`#vlan-id-input`, 5*time.Second),
		b.iframeClearAndType(`#vlan-id-input`, vlanID),
	)
}

// WizardConfigureSecondaryDNS fills the secondary DNS field on the Network step.
func (b *OnboardingBrowser) WizardConfigureSecondaryDNS(dns string) error {
	return chromedp.Run(b.ctx,
		b.iframeClearAndType(`#secondary-dns-ipv4`, dns),
	)
}

// WizardNavigateBackwards clicks the Back button n times. WizardClickBack already
// polls until the wizard's current step actually changes, so no extra delay
// between clicks is needed.
func (b *OnboardingBrowser) WizardNavigateBackwards(n int) error {
	for i := 0; i < n; i++ {
		if err := b.WizardClickBack(); err != nil {
			return fmt.Errorf("clicking Back (click %d/%d): %w", i+1, n, err)
		}
	}
	return nil
}

// StartCockpitTunnel starts an SSH local port forward from a free local port to
// the VM's Cockpit service on port 9090. Returns the local address to use with
// CockpitLogin and a cleanup function that kills the tunnel process.
//
// The forward targets the guest's own loopback ("localhost:9090") and binds
// locally to 127.0.0.1, so the browser reaches Cockpit as 127.0.0.1. This is the
// right choice for every spec that drives configuration through the multi-NIC
// inline apply path; use StartCockpitTunnelViaInterface for specs that need the
// wizard's single-NIC detection to fire.
func StartCockpitTunnel(sshPort int, sshUser, sshPassword string) (cockpitAddr string, cleanup func(), err error) {
	return startCockpitTunnel(sshPort, sshUser, sshPassword, "127.0.0.1", "localhost")
}

// StartCockpitTunnelViaInterface forwards to the guest's real interface address
// (guestIP:9090) instead of its loopback, and binds the local end to 127.0.0.2
// so the browser's window.location.hostname is "127.0.0.2" rather than a value
// the wizard treats as localhost. Both are required to make the wizard's
// single-NIC detection (isConnectedViaInterface in the onboarding plugin) return
// true: it bails out immediately when the page hostname is localhost, and it
// otherwise matches the local address of the accepted cockpit-ws connection
// (as reported by `ss`) against the interface's addresses. Forwarding to
// guestIP:9090 makes cockpit-ws see guestIP as that local address, which is a
// member of the interface's addresses on a single-NIC guest. The returned
// cockpitAddr is "127.0.0.2:<port>"; 127.0.0.2 is still loopback (bindable
// without extra setup on Linux) but is not one of the literals the plugin's
// isLocalhost() treats as local.
func StartCockpitTunnelViaInterface(sshPort int, sshUser, sshPassword, guestIP string) (cockpitAddr string, cleanup func(), err error) {
	return startCockpitTunnel(sshPort, sshUser, sshPassword, "127.0.0.2", guestIP)
}

// startCockpitTunnel is the shared implementation behind StartCockpitTunnel and
// StartCockpitTunnelViaInterface. localBind is the loopback address the forward
// listens on (and the host the browser navigates to); forwardHost is the host
// cockpit-ws is reached at from inside the guest.
func startCockpitTunnel(sshPort int, sshUser, sshPassword, localBind, forwardHost string) (cockpitAddr string, cleanup func(), err error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(localBind, "0"))
	if err != nil {
		return "", nil, fmt.Errorf("finding free port on %s: %w", localBind, err)
	}
	localPort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	// Pass the password via SSHPASS (sshpass -e) rather than on the command line
	// (sshpass -p), so the credential stays out of argv, the process table, and any
	// logged command string.
	cmd := exec.Command("sshpass", "-e", // #nosec G204 - e2e test code with controlled inputs
		"ssh", "-p", strconv.Itoa(sshPort),
		fmt.Sprintf("%s@127.0.0.1", sshUser),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-L", fmt.Sprintf("%s:%d:%s:%d", localBind, localPort, forwardHost, cockpitPort),
		"-N",
	)
	cmd.Env = append(os.Environ(), "SSHPASS="+sshPassword)
	// Capture ssh's stderr so a tunnel that never comes up (auth failure, refused
	// forward) reports the underlying reason instead of a bare "did not become
	// ready" timeout.
	var sshStderr bytes.Buffer
	cmd.Stderr = &sshStderr
	if startErr := cmd.Start(); startErr != nil {
		return "", nil, fmt.Errorf("starting SSH tunnel: %w", startErr)
	}

	dialAddr := net.JoinHostPort(localBind, strconv.Itoa(localPort))
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", dialAddr, 500*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			cleanup = func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
			return dialAddr, cleanup, nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return "", nil, fmt.Errorf("SSH tunnel to %s:%d via SSH port %d did not become ready within 15s: %s",
		forwardHost, cockpitPort, sshPort, strings.TrimSpace(sshStderr.String()))
}

// --- iframe helpers ---
// All wizard content lives inside a Cockpit iframe. These helpers execute
// JavaScript on the parent page, reach into the iframe's contentDocument,
// and perform DOM operations there.

func (b *OnboardingBrowser) iframeDoc() string {
	// Resolve the iframe defensively: document.querySelector returns null when the
	// Cockpit iframe is gone (session drop, reload, login timeout), and reading
	// .contentDocument on null throws a TypeError inside the page. Returning null
	// instead lets every helper's `if (!doc)` guard produce a diagnosable
	// "iframe not found" message rather than an opaque
	// "Cannot read properties of null (reading 'contentDocument')".
	return fmt.Sprintf(
		`(function(){var f=document.querySelector("iframe[name='%s']");return f?f.contentDocument:null;})()`,
		cockpitIframeName,
	)
}

// iframeWaitVisible polls until an element matching sel is visible inside the iframe.
func (b *OnboardingBrowser) iframeWaitVisible(sel string, timeout time.Duration) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		deadline := time.Now().Add(timeout)
		js := fmt.Sprintf(`
			(function() {
				var doc = %s;
				if (!doc) return false;
				var el = doc.querySelector('%s');
				if (!el) return false;
				var rect = el.getBoundingClientRect();
				return rect.width > 0 && rect.height > 0;
			})()
		`, b.iframeDoc(), escapeJSString(sel))

		for {
			var visible bool
			if err := chromedp.Evaluate(js, &visible).Do(ctx); err != nil {
				return fmt.Errorf("iframeWaitVisible(%s): %w", sel, err)
			}
			if visible {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("iframeWaitVisible(%s): timed out after %s", sel, timeout)
			}
			time.Sleep(250 * time.Millisecond)
		}
	})
}

// iframeWaitVisibleAny polls until any of the given selectors is visible inside
// the iframe. Useful when a page can settle into one of several states (e.g. the
// onboarding plugin renders the wizard on a fresh device but the already-complete
// screen once onboarding has finished).
func (b *OnboardingBrowser) iframeWaitVisibleAny(sels []string, timeout time.Duration) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		deadline := time.Now().Add(timeout)
		var conds []string
		for _, sel := range sels {
			conds = append(conds, fmt.Sprintf(`(function(){var el=doc.querySelector('%s');if(!el)return false;var r=el.getBoundingClientRect();return r.width>0&&r.height>0;})()`, escapeJSString(sel)))
		}
		js := fmt.Sprintf(`
			(function() {
				var doc = %s;
				if (!doc) return false;
				return %s;
			})()
		`, b.iframeDoc(), strings.Join(conds, " || "))

		for {
			var visible bool
			if err := chromedp.Evaluate(js, &visible).Do(ctx); err != nil {
				return fmt.Errorf("iframeWaitVisibleAny(%v): %w", sels, err)
			}
			if visible {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("iframeWaitVisibleAny(%v): timed out after %s", sels, timeout)
			}
			time.Sleep(250 * time.Millisecond)
		}
	})
}

// iframeClick clicks an element inside the iframe by CSS selector.
func (b *OnboardingBrowser) iframeClick(sel string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		js := fmt.Sprintf(`
			(function() {
				var doc = %s;
				if (!doc) throw new Error('iframe not found');
				var el = doc.querySelector('%s');
				if (!el) throw new Error('element not found: %s');
				el.scrollIntoView({block: 'center'});
				el.click();
				return true;
			})()
		`, b.iframeDoc(), escapeJSString(sel), escapeJSString(sel))

		return chromedp.Evaluate(js, nil).Do(ctx)
	})
}

// iframeClickXPath clicks an element inside the iframe by XPath.
func (b *OnboardingBrowser) iframeClickXPath(xpath string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		js := fmt.Sprintf(`
			(function() {
				var doc = %s;
				if (!doc) throw new Error('iframe not found');
				var result = doc.evaluate("%s", doc, null, XPathResult.FIRST_ORDERED_NODE_TYPE, null);
				var el = result.singleNodeValue;
				if (!el) throw new Error('element not found by XPath: %s');
				el.scrollIntoView({block: 'center'});
				el.click();
				return true;
			})()
		`, b.iframeDoc(), escapeJSString(xpath), escapeJSString(xpath))

		return chromedp.Evaluate(js, nil).Do(ctx)
	})
}

// footerButtonSelectorJS returns a JS expression that resolves the footer's
// primary ("Next"/"Apply"/"Enroll") or secondary ("Back") button. The primary
// button has a stable id (#wizard-next-btn) and carries the onMouseDown handler
// that authorizes the apply; prefer it over a class match. Falling back to the
// footer's own primary/secondary class also avoids matching the sidebar step-nav
// button labeled "Apply configuration".
func (b *OnboardingBrowser) footerButtonSelectorJS(variant string) string {
	if variant == "primary" {
		return `(doc.querySelector('#wizard-next-btn') || (footer && footer.querySelector('button.pf-m-primary')))`
	}
	return `(footer && footer.querySelector('button.pf-m-` + escapeJSString(variant) + `'))`
}

// iframeClickFooterButton clicks the PatternFly Wizard footer's primary or
// secondary button. It polls until the button exists and is enabled (the footer
// briefly disables the primary button while React transitions between steps and
// re-validates), then dispatches mousedown+mouseup, waits for React to re-render,
// and dispatches click in a SEPARATE task.
//
// The split matters for the Review step's Apply button. Its onMouseDown handler
// authorizes the apply (setState isApplyAuthorized=true), which is what makes the
// progress step reachable so the wizard's onNext can advance to it. A real user
// click has a browser paint/re-render between mousedown and click; if we instead
// dispatch mousedown→click synchronously in one task, React batches the
// authorize state update and it has not flushed by the time click's onNext runs,
// so onNext finds the progress step still unreachable and the wizard stays on
// Review forever. Emitting mousedown and click in separate evaluations, with a
// delay between, lets React process the authorize re-render first.
func (b *OnboardingBrowser) iframeClickFooterButton(variant string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		sel := b.footerButtonSelectorJS(variant)
		downJS := fmt.Sprintf(`
			(function() {
				var doc = %s;
				if (!doc) throw new Error('iframe not found');
				var footer = doc.querySelector('.pf-v6-c-wizard__footer');
				var btn = %s;
				if (!btn) return 'notfound';
				if (btn.disabled || btn.getAttribute('aria-disabled') === 'true') return 'disabled';
				btn.scrollIntoView({block: 'center'});
				btn.dispatchEvent(new MouseEvent('mousedown', {bubbles: true, cancelable: true, view: doc.defaultView}));
				btn.dispatchEvent(new MouseEvent('mouseup', {bubbles: true, cancelable: true, view: doc.defaultView}));
				return 'downed';
			})()
		`, b.iframeDoc(), sel)
		clickJS := fmt.Sprintf(`
			(function() {
				var doc = %s;
				if (!doc) throw new Error('iframe not found');
				var footer = doc.querySelector('.pf-v6-c-wizard__footer');
				var btn = %s;
				if (!btn) return 'notfound';
				btn.dispatchEvent(new MouseEvent('click', {bubbles: true, cancelable: true, view: doc.defaultView}));
				return 'clicked';
			})()
		`, b.iframeDoc(), sel)

		deadline := time.Now().Add(10 * time.Second)
		var last string
		for {
			var state string
			if err := chromedp.Evaluate(downJS, &state).Do(ctx); err != nil {
				return fmt.Errorf("iframeClickFooterButton(%s) mousedown: %w", variant, err)
			}
			last = state
			if state == "downed" {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("iframeClickFooterButton(%s): button %s after 10s", variant, last)
			}
			time.Sleep(200 * time.Millisecond)
		}
		// Let React flush the onMouseDown-triggered re-render (e.g. authorizeApply
		// enabling the progress step) before the click drives onNext.
		time.Sleep(350 * time.Millisecond)
		var clicked string
		if err := chromedp.Evaluate(clickJS, &clicked).Do(ctx); err != nil {
			return fmt.Errorf("iframeClickFooterButton(%s) click: %w", variant, err)
		}
		if clicked != "clicked" {
			return fmt.Errorf("iframeClickFooterButton(%s): click phase returned %q", variant, clicked)
		}
		return nil
	})
}

// iframeClearAndType clears a text input and types new text inside the iframe.
// Uses React-compatible input event dispatching.
func (b *OnboardingBrowser) iframeClearAndType(sel, text string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		js := fmt.Sprintf(`
			(function() {
				var doc = %s;
				if (!doc) throw new Error('iframe not found');
				var el = doc.querySelector('%s');
				if (!el) throw new Error('element not found: %s');
				el.scrollIntoView({block: 'center'});
				el.focus();

				// Use the React-compatible setter to update the value.
				var nativeInputValueSetter = Object.getOwnPropertyDescriptor(
					window.HTMLInputElement.prototype, 'value'
				);
				var nativeTextAreaValueSetter = Object.getOwnPropertyDescriptor(
					window.HTMLTextAreaElement.prototype, 'value'
				);
				var setter = (el.tagName === 'TEXTAREA' ? nativeTextAreaValueSetter : nativeInputValueSetter);
				if (setter && setter.set) {
					setter.set.call(el, '%s');
				} else {
					el.value = '%s';
				}
				el.dispatchEvent(new Event('input', {bubbles: true}));
				el.dispatchEvent(new Event('change', {bubbles: true}));
				return true;
			})()
		`, b.iframeDoc(), escapeJSString(sel), escapeJSString(sel),
			escapeJSString(text), escapeJSString(text))

		return chromedp.Evaluate(js, nil).Do(ctx)
	})
}

// iframeEnsureSwitchOn clicks a PatternFly Switch toggle if it's not already on.
// switchID is the element ID of the Switch input (without the '#' prefix).
func (b *OnboardingBrowser) iframeEnsureSwitchOn(switchID string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		js := fmt.Sprintf(`
			(function() {
				var doc = %s;
				if (!doc) throw new Error('iframe not found');
				var input = doc.querySelector('#%s');
				if (!input) throw new Error('switch not found: #%s');
				if (input.checked) return true;
				// PF Switch: click the label to toggle
				var label = doc.querySelector("label[for='%s']");
				if (label) {
					label.click();
				} else {
					input.click();
				}
				return true;
			})()
		`, b.iframeDoc(), escapeJSString(switchID), escapeJSString(switchID), escapeJSString(switchID))

		return chromedp.Evaluate(js, nil).Do(ctx)
	})
}

// iframeEnsureSwitchOff clicks a PatternFly Switch toggle if it's currently on.
// switchID is the element ID of the Switch input (without the '#' prefix).
func (b *OnboardingBrowser) iframeEnsureSwitchOff(switchID string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		js := fmt.Sprintf(`
			(function() {
				var doc = %s;
				if (!doc) throw new Error('iframe not found');
				var input = doc.querySelector('#%s');
				if (!input) throw new Error('switch not found: #%s');
				if (!input.checked) return true;
				// PF Switch: click the label to toggle
				var label = doc.querySelector("label[for='%s']");
				if (label) {
					label.click();
				} else {
					input.click();
				}
				return true;
			})()
		`, b.iframeDoc(), escapeJSString(switchID), escapeJSString(switchID), escapeJSString(switchID))

		return chromedp.Evaluate(js, nil).Do(ctx)
	})
}

// escapeJSString escapes single quotes and backslashes for safe embedding in JS strings.
func escapeJSString(s string) string {
	var result []byte
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			result = append(result, '\\', '\\')
		case '\'':
			result = append(result, '\\', '\'')
		case '"':
			result = append(result, '\\', '"')
		case '\n':
			result = append(result, '\\', 'n')
		case '\r':
			result = append(result, '\\', 'r')
		default:
			result = append(result, s[i])
		}
	}
	return string(result)
}
