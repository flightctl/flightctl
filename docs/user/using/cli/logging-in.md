# Logging into the Flight Control Service Using the CLI

This section explains how to authenticate to the Flight Control service using the `flightctl login` command. After successful login, your credentials are stored locally and used for all subsequent CLI operations.

## Logging in with Web-Based Authentication

Web-based authentication uses your browser to authenticate through the configured identity provider (OAuth2, OIDC, OpenShift, AAP, etc.). This is the recommended method for interactive use.

**Prerequisites:**

* You have installed the Flight Control CLI (see [Installing the CLI](../../installing/installing-cli.md))
* You have access to a web browser
* You have user credentials for the configured identity provider
* The OAuth/OIDC provider is configured to allow `http://localhost:8080/callback` as a redirect URL (or your custom callback port)

**Procedure:**

1. Log in to the Flight Control service:

   ```shell
   flightctl login https://flightctl.example.com --web
   ```

   A browser window opens automatically.

2. Authenticate using your identity provider credentials in the browser.

3. After successful authentication, the browser displays a success message and you can close the window.

4. Verify successful login:

   ```shell
   flightctl get devices
   ```

   If authentication was successful, the command displays your devices or an empty list if no devices are enrolled yet.

**Additional Options:**

* To use a specific authentication provider when multiple providers are configured:

  ```shell
  flightctl login https://flightctl.example.com --web --provider=corporate-sso
  ```

  To see available providers, use `--show-providers` (see [Listing Available Authentication Providers](#listing-available-authentication-providers)).

* To use a custom callback port:

  ```shell
  flightctl login https://flightctl.example.com --web --callback-port=9090
  ```

  Ensure the provider allows `http://localhost:9090/callback` as a redirect URL.

* To specify a custom CA certificate for the API server:

  ```shell
  flightctl login https://flightctl.example.com --web --certificate-authority=/path/to/ca.crt
  ```

* To specify a custom CA certificate for the authentication server (when different from API server):

  ```shell
  flightctl login https://flightctl.example.com --web \
    --certificate-authority=/path/to/api-ca.crt \
    --auth-certificate-authority=/path/to/auth-ca.crt
  ```

## Logging in with a Token

Token-based authentication is useful for automation, CI/CD pipelines, and non-interactive scenarios.

**Prerequisites:**

* You have installed the Flight Control CLI (see [Installing the CLI](../../installing/installing-cli.md))
* You have obtained a valid bearer token from your identity provider or administrator

**Procedure:**

1. Log in using your token:

   ```shell
   flightctl login https://flightctl.example.com --token=eyJhbGciOiJSUzI1NiIs...
   ```

2. Verify successful login:

   ```shell
   flightctl get devices
   ```

   If authentication was successful, the command displays your devices or an empty list if no devices are enrolled yet.

**Additional Options:**

* To specify a custom CA certificate:

  ```shell
  flightctl login https://flightctl.example.com --token=<your-token> --certificate-authority=/path/to/ca.crt
  ```

## Logging in with Username and Password

Username and password authentication uses the OAuth/OIDC resource owner password credentials flow. This method is only available if your identity provider supports password flow.

**Prerequisites:**

* You have installed the Flight Control CLI (see [Installing the CLI](../../installing/installing-cli.md))
* Your identity provider supports the OAuth/OIDC password flow
* You have a username and password for the identity provider

**Procedure:**

1. Log in with your credentials:

   ```shell
   flightctl login https://flightctl.example.com -u myuser -p mypassword
   ```

2. Verify successful login:

   ```shell
   flightctl get devices
   ```

   If authentication was successful, the command displays your devices or an empty list if no devices are enrolled yet.

**Additional Options:**

* To use a specific authentication provider when multiple providers are configured:

  ```shell
  flightctl login https://flightctl.example.com -u myuser -p mypassword --provider=corporate-sso
  ```

  To see available providers, use `--show-providers` (see [Listing Available Authentication Providers](#listing-available-authentication-providers)).

* To specify a custom CA certificate:

  ```shell
  flightctl login https://flightctl.example.com -u myuser -p mypassword --certificate-authority=/path/to/ca.crt
  ```

## Listing Available Authentication Providers

If multiple authentication providers are configured on the server, you can list them to see which providers are available and choose which one to use for authentication.

**Prerequisites:**

* You have installed the Flight Control CLI (see [Installing the CLI](../../installing/installing-cli.md))
* You have network access to the Flight Control API server

**Procedure:**

1. List available providers:

   ```shell
   flightctl login https://flightctl.example.com --show-providers
   ```

   The command displays a table of available authentication providers with their names and display names.

2. Log in using a specific provider (replace `corporate-sso` with the provider name from the list):

   ```shell
   flightctl login https://flightctl.example.com --web --provider=corporate-sso
   ```

3. Verify successful login:

   ```shell
   flightctl get devices
   ```

   If authentication was successful, the command displays your devices or an empty list if no devices are enrolled yet.

## Using Insecure Connections (Development Only)

For development and testing environments with self-signed certificates, you can skip TLS certificate verification.

> **Warning:** Never use this option in production environments. Skipping certificate verification makes your connection insecure and vulnerable to man-in-the-middle attacks.

**Prerequisites:**

* You have installed the Flight Control CLI (see [Installing the CLI](../../installing/installing-cli.md))
* You are working in a development or testing environment

**Procedure:**

1. Log in with TLS verification disabled:

   ```shell
   flightctl login https://flightctl.example.com --web --insecure-skip-tls-verify
   ```

2. Verify successful login:

   ```shell
   flightctl get devices
   ```

## Configuration File Location

After successful login, your credentials are stored in the client configuration file:

* **Default location:** `~/.config/flightctl/client.yaml`
* **Contents:** Server URL, authentication tokens, TLS settings, selected organization

To use a non-default configuration file location, use the `--config` flag with any `flightctl` command.

## See Also

* [CLI Command Reference](../../references/cli-commands.md) - Complete CLI command syntax and flags
* [Configuring Authentication](../../installing/configuring-auth/overview.md) - Server-side authentication configuration
* [Managing Devices](../managing-devices.md) - Working with devices after login
* [Managing Fleets](../managing-fleets.md) - Working with fleets after login
