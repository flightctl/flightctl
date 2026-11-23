# How to Rotate Credentials

This document outlines the infrastructure credentials, robot accounts, and GitHub App secrets used across the `flightctl` GitHub organization. It also provides the standard operating procedures for auditing and safely rotating these credentials.

## Credential Inventory

### Quay.io Organizations and Robot Accounts

Our CI pipelines rely on three separate Quay.io organizations/namespaces:

| Quay Org | Robot Account | Purpose |
| :--- | :--- | :--- |
| `flightctl` | `flightctl+github` | The primary CI robot. Pushes core backend containers (API, worker, agent) and Helm charts. Used by `flightctl`, `flightctl-mcp`, and `vm-to-quadlet`. |
| `flightctl` | `flightctl+flightctlui` | UI-specific robot. Pushes standalone and OCP UI containers. Used by `flightctl-ui`. |
| `flightctl-demos` | `flightctl-demos+publisher` | Used for pushing demonstration bootc OS disk images and ISOs. Used by `flightctl-demos`. |
| `flightctl-tests` | `flightctl-tests+flightctl_tests_rb` | **Owned by QE team**. Used to build test agents and `packit-builder` images. Used by the main `flightctl` repository's E2E and RPM testing workflows. |

### GitHub Repositories and Secrets

The following are the actively used authentication and authorization secrets across our repositories. *(Note: Cryptographic signing keys like GPG and Sigstore are excluded here as they are identity keys, not standard auth tokens).*

| Repository | Secret Name | Purpose |
| :--- | :--- | :--- |
| **`flightctl`** | `QUAY_FLIGHTCTL_INFRA_ROBOT_USERNAME` / `PASSWORD` | Maps to `flightctl+github`. Pushes core containers and charts. |
| | `QUAY_FLIGHTCTL_TESTS_INFRA_ROBOT_USERNAME` / `PASSWORD` | Maps to `flightctl-tests+flightctl_tests_rb`. Pushes E2E test containers and RPM builders. |
| | `REPORTS_DEPLOY_KEY` | SSH private key to push test health reports to GitHub Pages. |
| **`flightctl-demos`** | `OCI_REGISTRY_USERNAME` / `PASSWORD` | Maps to `flightctl-demos+publisher`. Pushes bootc disk images. |
| | `APP_ID` / `APP_PRIVATE_KEY` | Credentials for the `flightctl-demos-automation` GitHub App. Used to generate tokens for auto-creating dependency update PRs. |
| **`flightctl-ui`** | `QUAY_FLIGHTCTL_INFRA_ROBOT_USERNAME` / `PASSWORD` | Maps to `flightctl+flightctlui`. Pushes UI containers. |
| | `NPMJS_FLIGHTCTL_RELEASE_TOKEN` | Auth token for publishing NPM packages. |
| **`flightctl-mcp`** | `QUAY_USERNAME` / `QUAY_PASSWORD` | Maps to `flightctl+github`. Pushes MCP server container. |
| **`vm-to-quadlet`** | `QUAY_USERNAME` / `QUAY_PASSWORD` | Maps to `flightctl+github`. Pushes the `vm-to-quadlet` container. |
| **`cockpit-onboarding`** | `COCKPIT_LIB_UPDATE_TOKEN` | GitHub PAT/token used for cockpit lib updates. |

---

## Auditing Secret Ages

You can use the `hack/list_secrets.sh` script to audit all repository secrets in the `flightctl` organization. It supports an optional `--older-than <days>` parameter to easily flag secrets that are overdue for rotation.

**Prerequisites:**

* You must have the GitHub CLI (`gh`) installed.
* You must be authenticated (`gh auth login`) with an account that has **Owner** permissions on the org/repos in order to read secrets across repositories.

Run the script to list secrets and their age:

```bash
./hack/list_secrets.sh
```

Run the script to list secrets older than 4 days:

```bash
./hack/list_secrets.sh --older-than 4
```

---

## Rotation Procedures

### General: Regenerating a Robot Account Password in Quay.io

1. Log into [Quay.io](https://quay.io).
2. Click on the target organization (`flightctl`, `flightctl-demos`, or `flightctl-tests`) from your left-hand menu or dashboard.
3. Click the **Robot Accounts** tab.
4. Click on the account name.
5. In the "Credentials for $robot_account", click the "Regenerate token now" button. Doing this immediately invalidates the old token.
6. Copy the new token to use in the steps below.

### Staged Rotation Process

To limit the blast radius, apply credentials in stages. **Note: We re-apply the usernames, App IDs, etc. in these commands to ensure consistency and self-documentation, even if they haven't changed.**

#### Stage 1: `flightctl-demos`

**1. Quay.io Robot:**
Rotate the password for `flightctl-demos+publisher` in Quay, then run:

```bash
gh secret set OCI_REGISTRY_USERNAME -R flightctl/flightctl-demos --body "flightctl-demos+publisher"
gh secret set OCI_REGISTRY_PASSWORD -R flightctl/flightctl-demos
# (Paste the new Quay password when prompted)
```

**2. GitHub App (`flightctl-demos-automation`):**
Navigate to the GitHub Org settings -> Developer Settings -> GitHub Apps -> `flightctl-demos-automation` -> **Key pairs**. Generate a new key (downloads a `.pem` file). **Before deleting the old key**, configure the repository to use the new key:

```bash
gh secret set APP_ID -R flightctl/flightctl-demos --body "2689755"
gh secret set APP_PRIVATE_KEY -R flightctl/flightctl-demos < /path/to/downloaded-private-key.pem
```

After updating the secret, you can safely delete the old key pair from the GitHub App settings UI.

#### Stage 2: `flightctl-ui`

Rotate the password for `flightctl+flightctlui` in Quay, then run:

```bash
gh secret set QUAY_FLIGHTCTL_INFRA_ROBOT_USERNAME -R flightctl/flightctl-ui --body "flightctl+flightctlui"
gh secret set QUAY_FLIGHTCTL_INFRA_ROBOT_PASSWORD -R flightctl/flightctl-ui
# (Paste the new Quay password when prompted)
```

#### Stage 3: General Infrastructure (`flightctl+github`)

Because the `flightctl+github` robot is shared across three repositories, store the newly regenerated Quay password in an environment variable to apply it seamlessly.

```bash
echo -n "Paste the new flightctl+github password: " && read -s NEW_GITHUB_ROBOT_PW && echo

# Apply to flightctl-mcp
gh secret set QUAY_USERNAME -R flightctl/flightctl-mcp --body "flightctl+github"
echo "$NEW_GITHUB_ROBOT_PW" | gh secret set QUAY_PASSWORD -R flightctl/flightctl-mcp

# Apply to vm-to-quadlet
gh secret set QUAY_USERNAME -R flightctl/vm-to-quadlet --body "flightctl+github"
echo "$NEW_GITHUB_ROBOT_PW" | gh secret set QUAY_PASSWORD -R flightctl/vm-to-quadlet

# Apply to flightctl (main repo)
gh secret set QUAY_FLIGHTCTL_INFRA_ROBOT_USERNAME -R flightctl/flightctl --body "flightctl+github"
echo "$NEW_GITHUB_ROBOT_PW" | gh secret set QUAY_FLIGHTCTL_INFRA_ROBOT_PASSWORD -R flightctl/flightctl

unset NEW_GITHUB_ROBOT_PW
```

#### Stage 4: Test Infrastructure (`flightctl-tests`)

*Note: The `flightctl-tests` Quay organization is owned by the QE team. Coordinate with QE to regenerate this robot's password.*

```bash
gh secret set QUAY_FLIGHTCTL_TESTS_INFRA_ROBOT_USERNAME -R flightctl/flightctl --body "flightctl-tests+flightctl_tests_rb"
gh secret set QUAY_FLIGHTCTL_TESTS_INFRA_ROBOT_PASSWORD -R flightctl/flightctl
# (Paste the new QE Quay password when prompted)
```
