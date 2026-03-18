# Auth provider E2E suite

This suite tests authentication provider (dynamic OIDC) and the OAuth authorization-code flow.

- **Runs on:** both K8s (kind) and Quadlet. Bootstrap login uses `login.LoginToAPIWithToken` (K8s token on kind, PAM on Quadlet).
- **BeforeSuite:** starts Keycloak (aux service only), applies a dynamic OIDC AuthProvider CR pointing at Keycloak.
- **Specs:** use `flightctl login --provider keycloak-e2e --web --no-browser`; parse the printed auth URL; drive a headless browser (chromedp) to the Keycloak login page, fill username/password, submit; the CLI callback receives the redirect and completes login. Then verify an API call (e.g. `flightctl get devices`) succeeds with the Keycloak token.

Requires Chrome/Chromium for chromedp (headless). The suite uses `e2e.SetupWorkerHarnessWithoutVM()` (no device VM).
