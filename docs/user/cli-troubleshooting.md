# CLI Troubleshooting Guide

This guide helps you resolve common issues when using the Flight Control CLI (`flightctl`).

## Table of Contents

- [Login Issues](#login-issues)
- [URL Format Problems](#url-format-problems)
- [Network Connectivity Issues](#network-connectivity-issues)
- [Authentication Problems](#authentication-problems)
- [Certificate Issues](#certificate-issues)
- [Getting Help](#getting-help)

## Login Issues

### Common Login Error Messages

#### "API URL contains an unexpected path component '%s'. The API URL should only contain the hostname and optionally a port. Try: %s"

**Problem**: You've included a path component in the API URL that shouldn't be there.

**Example**:

```bash
flightctl login https://api.example.com/devicemanagement/devices
```

**Solution**: Remove the path component from the URL. The API URL should only contain the hostname and optionally a port.

```bash
# Correct format
flightctl login https://api.example.com
```

**Why this happens**: The Flight Control API is served from the root path of the server, not from subdirectories.

#### "the API URL must use HTTPS for secure communication. Please ensure the API URL starts with 'https://' and try again"

**Problem**: You're trying to use HTTP instead of HTTPS.

**Example**:

```bash
flightctl login http://api.example.com
```

**Solution**: Always use HTTPS for secure communication.

```bash
# Correct format
flightctl login https://api.example.com
```

#### "API URL is missing a valid hostname. Please provide a complete URL with hostname"

**Problem**: The URL doesn't contain a valid hostname.

**Example**:

```bash
flightctl login https://
```

**Solution**: Provide a complete URL with a valid hostname.

```bash
# Correct format
flightctl login https://api.example.com
```

## URL Format Problems

### Valid URL Formats

The Flight Control CLI accepts URLs in these formats:

```bash
# Basic HTTPS URL
flightctl login https://api.example.com

# HTTPS URL with port
flightctl login https://api.example.com:8443

# HTTPS URL with custom port (non-standard)
flightctl login https://api.example.com:9443
```

### Invalid URL Formats

```bash
# ❌ Don't include path components
flightctl login https://api.example.com/api/v1

# ❌ Don't use HTTP
flightctl login http://api.example.com

# ❌ Don't include query parameters
flightctl login https://api.example.com?param=value

# ❌ Don't include fragments
flightctl login https://api.example.com#section
```

## Network Connectivity Issues

### "cannot connect to the API server"

**Problem**: The CLI cannot establish a connection to the API server.

**Possible causes**:

- The server is down or not running
- The URL is incorrect
- Network connectivity issues
- Firewall blocking the connection

**Troubleshooting steps**:

1. **Verify the server is running**:

   ```bash
   # Check if the server is accessible
   curl https://api.example.com/health
   # If your deployment uses a private CA:
   # curl --cacert /path/to/ca.crt https://api.example.com/health
   # Development-only fallback (not recommended):
   # curl -k https://api.example.com/health
   ```

2. **Check network connectivity**:

   ```bash
   # Test basic connectivity
   ping api.example.com
   
   # Test port connectivity
   telnet api.example.com 443
   ```

3. **Verify the URL**:
    - Ensure you're using the correct hostname
    - Check if you need to use a different port
    - Verify the protocol (HTTPS)

### "cannot resolve hostname"

**Problem**: The DNS cannot resolve the hostname.

**Possible causes**:

- Incorrect hostname
- DNS configuration issues
- Network connectivity problems

**Troubleshooting steps**:

1. **Check the hostname**:

   ```bash
   # Verify the hostname is correct
   nslookup api.example.com
   ```

2. **Try using IP address temporarily**:

   ```bash
   # If DNS is the issue, try using IP directly
   flightctl login https://192.168.1.100
   ```

3. **Check DNS configuration**:

   ```bash
   # Check your DNS servers
   cat /etc/resolv.conf
   ```

### "connection timed out"

**Problem**: The connection to the server is timing out.

**Possible causes**:

- Slow network connection
- Server overload
- Firewall blocking the connection
- Incorrect port

**Troubleshooting steps**:

1. **Check network speed**:

   ```bash
   # Test network speed to the server
   curl -w "@-" -o /dev/null -s https://api.example.com <<< "time_total: %{time_total}\n"
   ```

2. **Try different ports**:

   ```bash
   # Default Flight Control ports
   flightctl login https://api.example.com:3443  # API port (default)
   flightctl login https://api.example.com:7443  # Agent port (default)
   
   # Custom port example
   flightctl login https://api.example.com:9443  # Custom port
   ```

   > Note: These are the default ports (configured in `internal/config/config.go`); your installation may use different ports.

3. **Check firewall settings**:

   ```bash
   # Test if port is reachable
   nc -zv api.example.com 443
   ```

## Authentication Problems

### "authentication failed"

**Problem**: The provided credentials are incorrect or the authentication method is not supported.

**Troubleshooting steps**:

1. **Verify credentials**:
    - Double-check username and password
    - Ensure the account exists and is active
    - Check if the account has the necessary permissions

2. **Try different authentication methods**:

   ```bash
   # Try web-based authentication
   flightctl login https://api.example.com --web
   
   # Try with token
   flightctl login https://api.example.com --token=<your-token>
   ```

3. **Check authentication provider**:
    - Verify the authentication provider is configured correctly
    - Check if the provider is accessible

### "must provide --token"

**Problem**: The server requires token-based authentication but no token was provided.

**Solution**: Obtain a valid token and use it for authentication.

```bash
# For OpenShift/ACM deployments
flightctl login https://api.example.com -t $(oc whoami -t)

# For other deployments, obtain token from your admin
flightctl login https://api.example.com --token=<your-token>
```

## Certificate Issues

### "TLS certificate error"

**Problem**: The server's TLS certificate cannot be verified.

**Possible causes**:

- Self-signed certificate
- Certificate from unknown CA
- Certificate expired
- Certificate name mismatch

**Solutions**:

1. **Provide CA certificate** (recommended):

   ```bash
   flightctl login https://api.example.com --certificate-authority=/path/to/ca.crt
   ```

2. **Add CA to system trust store**:

   ```bash
   # Copy CA certificate to system trust store
   sudo cp ca.crt /usr/local/share/ca-certificates/
   sudo update-ca-certificates
   ```

3. **Use insecure flag (development only)**:

   ```bash
   flightctl login https://api.example.com --insecure-skip-tls-verify
   ```

### "certificate signed by unknown authority"

**Problem**: The certificate is signed by a CA not trusted by your system.

**Solution**: Add the CA certificate to your system's trust store or use the `--certificate-authority` flag.

## Getting Help

### Additional Resources

- [Getting Started Guide](getting-started.md) - Complete setup instructions
- [Install CLI Guide](install-cli.md) - CLI installation instructions
- [Network Requirements](network-requirements.md) - Network configuration details
- [Troubleshooting](troubleshooting.md) - General troubleshooting guide

### Command Help

Get help for any command:

```bash

flightctl --help

# Command-specific help
flightctl login --help
flightctl get --help
flightctl apply --help
```

### Logs

Check logs for additional information:

```bash

# Agent logs (if running as a service)

# System logs (for network/TLS/systemd issues)
journalctl -f
```

### Support

If you continue to experience issues:

1. **Collect diagnostic information**:

   ```bash
   # Get system information
   flightctl version
   uname -a
   cat /etc/os-release
   ```

2. **Generate SOS report** (if applicable):

   ```bash

   # Using sos from PATH
   flightctl console device/1234-abcd -- sos report --batch --quiet
   # Using full path to sos
   flightctl console device/1234-abcd -- /usr/sbin/sos report --batch --quiet
   ```

3. **Contact support** with:
    - Error messages
    - CLI version
    - System information
    - Steps to reproduce the issue
