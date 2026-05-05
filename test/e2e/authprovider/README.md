# Auth provider E2E suite

This suite tests the `EDM-2117` browser-login slice for authentication providers using real provider implementations that the suite can own and validate.

- **Runs on:** kind, OCP, and Quadlet. Bootstrap login uses `login.LoginToAPIWithToken` (K8s token on kind, OpenShift token on OCP, PAM on Quadlet).
- **BeforeSuite:** starts Keycloak (aux service only), applies a dynamic OIDC AuthProvider CR pointing at Keycloak.
- **Specs covered by the suite:**
  - **Generic OIDC / dynamic AuthProvider:** Keycloak as the representative OIDC IdP
  - **Generic OAuth2 / dynamic AuthProvider:** suite-owned Keycloak-backed OAuth2 provider with its own client
  - **OpenShift browser login:** OCP only, using the deployment's built-in OpenShift auth provider
  - **PAM issuer browser login:** Quadlet only, using the bundled PAM OIDC provider
  - **Dynamic OIDC lifecycle:** provider visibility, enable/disable, deletion, duplicate rejection
- **Feature-scope but not fully validated in this suite yet:** OAuth2 beyond the suite-owned Keycloak-backed case. Jira children confirm it is part of the `EDM-2117` browser-login feature slice:
  - `EDM-2373`: multiple providers when logging in; latest comment says the flow triggers correctly for K8s, OIDC, and OAuth2, with OpenShift still needing verification
  - `EDM-2603`: dynamic OpenShift-as-OAuth2 login flow exercised through UI/CLI
  - `EDM-2701`: OAuth2 token validation/login behavior

## Cypress setup

The Keycloak OIDC and Keycloak-backed OAuth2 browser-login specs remain `chromedp`-based.

The OpenShift, PAM, and AAP browser-login specs are driven by a suite-local Cypress harness under [`test/e2e/authprovider/cypress`](./cypress).

The Go suite invokes [`run-provider-login-cypress.sh`](./cypress/run-provider-login-cypress.sh) for the OpenShift, PAM, and AAP browser-login specs.
If Cypress is missing, that wrapper installs it automatically with `npm install` before running the test.
The wrapper prefers credentials from `USERNAME` and `PASSWORD` environment variables and only falls back to positional arguments if they are provided. The callback port defaults to the CLI default `8080` unless `FLIGHTCTL_CALLBACK_PORT` overrides it.

Requires Chrome/Chromium for the existing `chromedp` Keycloak flow. The suite uses `e2e.SetupWorkerHarnessWithoutVM()` (no device VM).

## Credential sources

- **Keycloak / Generic OIDC:** suite-owned credentials from the test realm (`testuser` / `testpass`)
- **Keycloak / Generic OAuth2:** suite-owned credentials from the test realm (`testuser` / `testpass`) and the suite-owned `flightctl-oauth2-client`
- **OpenShift:** `OPENSHIFT_USERNAME` / `OPENSHIFT_PASSWORD`, falling back to `kubeadmin` and `KUBEADMIN_PASS`
- **PAM:** `E2E_PAM_USER` / `E2E_PAM_PASSWORD`, with the same defaults used by the repo
- **AAP:** `AAP_USERNAME` / `AAP_PASSWORD` (required, no defaults; test is skipped if not set). Quadlet AAP setup also requires `AAP_API_URL` and either `AAP_CLIENT_ID` or `AAP_TOKEN`; optional overrides are `AAP_AUTHORIZATION_URL`, `AAP_TOKEN_URL`, `AAP_APP_NAME`, and `AAP_ORGANIZATION_ID`.
