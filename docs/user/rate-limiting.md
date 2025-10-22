# Rate Limiting

Flight Control implements rate limiting to protect the API from abuse and ensure fair usage. This document explains how rate limiting works and how to configure it properly.

## Overview

Rate limiting in Flight Control is a combination of strategies applied at different endpoints:

- IP-based limits for endpoints before authentication (when feasible)
- Identity-based limits (by username/UID or device fingerprint) for authenticated endpoints

## How It Works

### IP Detection

Flight Control automatically detects the real client IP address using the following priority order:

1. **True-Client-IP header**
2. **X-Real-IP header**
3. **X-Forwarded-For header** (most common with reverse proxies)
4. **Direct connection IP** (fallback)

### Rate Limit Types

Flight Control applies different rate limits per endpoint class:

- **General API requests (authenticated users)**: Identity-based limiting using user identity (username or UID)
- **Agent API (mTLS devices)**: Identity-based limiting using device fingerprint from the client certificate
- **Authentication endpoints (before authentication)**: IP-based limiting when the real client IP is available. If a proxy hides the real IP, in-app IP-based limiting may be ineffective (see the OpenShift section below).
- **Public/read-only endpoints**: Typically no limits or a very generous cap; consider per-connection token bucket if needed

See Endpoint-specific Strategies below for details.

## Endpoint-specific Strategies

The following guidance describes how Flight Control applies rate limiting per endpoint type and what to configure in different environments.

### 1) Authenticated API (users)

- Key: `user:<username>` if available, otherwise `uid:<uid>`
- Rationale: Multiple users behind one proxy should not share a bucket
- Behavior: Applied after authentication succeeds

Example behavior:

- Different authenticated users from the same IP each get independent limits
- If username is empty, UID is used as a stable identity

### 2) Agent API (devices)

- Key: `device:<fingerprint>` where fingerprint is extracted from the device certificate extension
- Rationale: Distinct devices behind NAT/routers should not interfere with each other
- Behavior: Applied after successful client-certificate auth

### 3) Authentication endpoints (before authentication)

- Purpose: Protect login/validation endpoints from brute force
- Preferred: IP-based limits when the real client IP is available (e.g., via trusted proxy headers)

Notes:

- If your deployment topology hides the real client IP (for example, certain TLS passthrough setups), in-app IP-based auth limiting may be ineffective
- In those cases, prefer enforcing rate limiting at the router/load balancer; see the OpenShift section below for guidance

## Configuration

Rate limiting is configured in your Flight Control configuration file:

```yaml
service:
  rateLimit:
    # General API rate limiting (applies to authenticated API requests)
    requests: 300       # Maximum requests per window
    window: "1m"        # Time window (e.g., "1m", "1h", "1d")
    
    # Authentication-specific rate limiting (stricter)
    authRequests: 20    # Maximum auth requests per window
    authWindow: "1h"    # Auth time window
    
    # Trusted proxies that can set True-Client-IP/X-Forwarded-For/X-Real-IP headers
    # This should include your load balancer and UI proxy IPs
    trustedProxies:
      - "10.0.0.0/8"     # Internal network range
      - "172.16.0.0/12"  # Docker/container network range
      - "192.168.0.0/16" # Private network range
```

### Default Values

The default rate limiting behavior depends on your deployment method:

- **Helm deployments**: Rate limiting is **enabled by default** with the following values:
  - General API: 300 requests per minute
  - Authentication: 20 requests per hour
- **Quadlet deployments**: Rate limiting is **enabled by default** with the same values as Helm
- **Manual configuration**: If not configured, rate limiting is **disabled by default**. To enable it, you must explicitly set the configuration values.

**Important**: When using reverse proxies (load balancers, API gateways, etc.), you **must** configure `trustedProxies` to include the IP ranges of your proxy infrastructure. Without this configuration, proxy headers will be ignored for security reasons, and all requests will be rate-limited based on the proxy's IP address rather than the real client IP.

### Environment Variables

You can also configure rate limiting using environment variables:

```bash
# General rate limiting
export RATE_LIMIT_REQUESTS=300
export RATE_LIMIT_WINDOW=1m

# Auth-specific rate limiting
export AUTH_RATE_LIMIT_REQUESTS=20
export AUTH_RATE_LIMIT_WINDOW=1h

# Trusted proxies (comma-separated list)
export RATE_LIMIT_TRUSTED_PROXIES="10.0.0.0/8,172.16.0.0/12,192.168.0.0/16"
```

**Important**: Environment variables **override** settings in the configuration file. The configuration loading process works as follows:

1. **Config file is loaded first** with default values
2. **Environment variables are processed second** and override any corresponding config file settings
3. **Only non-empty environment variables** override config file values

**Note**: The `RATE_LIMIT_TRUSTED_PROXIES` environment variable accepts a comma-separated list of CIDR ranges. Each CIDR should be in standard format (e.g., `10.0.0.0/8`, `172.16.0.0/12`).

**Example**: If your config file has `requests: 100` but you set `RATE_LIMIT_REQUESTS=300`, the final value will be `300` (the environment variable takes precedence).

## Deployment-Specific Configuration

The way you configure rate limiting depends on how Flight Control is deployed:

### Helm Deployments

For Helm deployments, rate limiting is configured in the `values.yaml` file or through Helm values:

**Location**: `deploy/helm/flightctl/values.yaml`

```yaml
api:
  rateLimit:
    # General API rate limiting
    requests: 300       # Maximum requests per window
    window: "1m"        # Time window
    # Auth-specific rate limiting
    authRequests: 20    # Maximum auth requests per window
    authWindow: "1h"    # Auth time window
    # Trusted proxies
    trustedProxies:
      - "10.0.0.0/8"
      - "172.16.0.0/12"
      - "192.168.0.0/16"
```

**To modify rate limiting in Helm:**

1. **Edit values.yaml directly**:

   ```bash
   # Edit the values file
   vim deploy/helm/flightctl/values.yaml
   
   # Update the deployment
   helm upgrade flightctl ./deploy/helm/flightctl
   ```

2. **Override values during deployment**:

   ```bash
   helm install flightctl ./deploy/helm/flightctl \
     --set api.rateLimit.requests=600 \
     --set api.rateLimit.authRequests=30
   ```

3. **Override values during upgrade**:

   ```bash
   helm upgrade flightctl ./deploy/helm/flightctl \
     --set api.rateLimit.requests=600 \
     --set api.rateLimit.authRequests=30
   ```

**To disable rate limiting in Helm:**

```bash
# Remove the rateLimit section entirely
helm upgrade flightctl ./deploy/helm/flightctl \
  --set api.rateLimit=null
```

**Note**: Setting `requests=0` or `authRequests=0` will **not** disable rate limiting. Instead, it will use the hard-coded default values (300 requests/minute for general API, 20 requests/hour for auth). To truly disable rate limiting, you must remove the entire `rateLimit` section.

### Quadlet Deployments

For Quadlet deployments, rate limiting is configured in the global service configuration:

**Location**: `deploy/podman/service-config.yaml`

```yaml
service:
  rateLimit:
    # General API rate limiting
    requests: 300
    window: "1m"
    # Auth-specific rate limiting
    authRequests: 20
    authWindow: "1h"
    # Trusted proxies
    trustedProxies:
      - "10.0.0.0/8"
      - "172.16.0.0/12"
      - "192.168.0.0/16"
```

**To modify rate limiting in Quadlets:**

1. **Edit the configuration file**:

   ```bash
   # Edit the service configuration
   sudo vim /etc/flightctl/service-config.yaml
   
   # Restart the API service
   sudo systemctl restart flightctl-api.service
   ```

2. **For RPM installations**, the configuration is in `/etc/flightctl/service-config.yaml`:

   ```bash
   # Edit the configuration
   sudo vim /etc/flightctl/service-config.yaml
   
   # Restart services
   sudo systemctl restart flightctl.target
   ```

**To disable rate limiting in Quadlets:**

```yaml
# Remove the rateLimit section entirely
service:
  # rateLimit section removed
```

**Note**: Setting `requests=0` or `authRequests=0` will **not** disable rate limiting. Instead, it will use the hard-coded default values (300 requests/minute for general API, 20 requests/hour for auth). To truly disable rate limiting, you must remove the entire `rateLimit` section.

## Reverse Proxy Configuration

When using a reverse proxy (nginx, HAProxy, load balancer, etc.), you have two options for proper rate limiting in front of Flight Control:

### Option 1: Configure Proxy to Use Real Client IPs

Configure your reverse proxy to rewrite the source IP address so that Flight Control sees the real client IP directly. This approach doesn't require proxy headers or trusted proxy configuration.

**Example (nginx with real_ip module):**

```nginx
location / {
    proxy_pass http://flightctl-api:3443;
    proxy_set_real_ip_from 10.0.0.0/8;  # Trusted proxy range
    proxy_set_real_ip_from 172.16.0.0/12;
    real_ip_header X-Forwarded-For;
    real_ip_recursive on;
}
```

### Option 2: Configure Proxy to Pass Headers + Set Trusted Proxies

Configure your reverse proxy to pass proxy headers, and configure Flight Control to trust those headers from your proxy infrastructure.

**Step 1: Configure your reverse proxy to send headers:**

```nginx
location / {
    proxy_pass http://flightctl-api:3443;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Real-IP $remote_addr;
}
```

**Step 2: Configure Flight Control to trust your proxy:**

```yaml
service:
  rateLimit:
    trustedProxies:
      - "10.0.0.0/8"     # Your proxy IP range
      - "172.16.0.0/12"  # Container network
```

**Important**: Without configuring `trustedProxies`, proxy headers will be silently ignored, and rate limiting will be based on the proxy's IP address rather than the real client IP.

## OpenShift Deployments (TLS Passthrough)

When using OpenShift Routes with `termination: passthrough`, TLS terminates at the Flight Control pod. The OpenShift router cannot inject HTTP headers (traffic is encrypted), and it does not forward PROXY protocol to pods. Therefore, the application cannot learn the real client IP from the router.

Implications:

- In app IP-based rate limiting on endpoints before authentication (e.g., `/api/v1/auth/validate`) is ineffective because all requests appear to originate from the router
- Identity-based rate limiting for authenticated endpoints continues to work normally (username/UID or device fingerprint)

Note: Identity-based rate limiting for authenticated endpoints (users and devices) does not rely on client IP addresses and works correctly in all deployment scenarios, including OpenShift passthrough. This provides fine-grained, per-user/per-device protection. Only the endpoints before authentication are affected by the IP detection limitation.

Recommended approach for OpenShift:

1) Keep identity-based rate limiting in-app for authenticated endpoints (already effective)
2) Disable/avoid in-app IP-based rate limiting for the auth endpoint when using passthrough Routes
3) Enforce auth endpoint rate limiting at the router (TCP-level):
   - Configure the external load balancer to send PROXY protocol to the OpenShift routers
   - Configure the `IngressController` to accept PROXY protocol (e.g., `spec.endpointPublishingStrategy.nodePort.protocol: PROXY`)
   - Use HAProxy router rate-limiting annotations on the Route (if supported in your OpenShift version), for example:

```yaml
metadata:
  annotations:
    haproxy.router.openshift.io/rate-limit-connections: "true"
    haproxy.router.openshift.io/rate-limit-connections.rate-tcp: "10"           # per-client-IP connections per 3s
    haproxy.router.openshift.io/rate-limit-connections.concurrent-tcp: "5"      # per-client-IP concurrent connections
```

Note: TCP-level rate limiting (`rate-tcp` and `concurrent-tcp`) is coarser than application-level rate limiting but is the only option for passthrough routes. It limits connections per client IP rather than HTTP requests, so it may be less precise for rate limiting authentication attempts.

## Rate Limit Headers

Flight Control includes rate limit information in response headers:

- `X-RateLimit-Limit`: Maximum requests allowed per window
- `X-RateLimit-Remaining`: Number of requests remaining in the current window
- `X-RateLimit-Reset`: Timestamp when the rate limit window resets
- `Retry-After`: Seconds to wait before retrying (when rate limited)

### Example Response Headers

**Successful request:**

```text
X-RateLimit-Limit: 60
X-RateLimit-Remaining: 45
X-RateLimit-Reset: 1640995200
```

**Rate limited request:**

```text
X-RateLimit-Limit: 60
X-RateLimit-Remaining: 0
X-RateLimit-Reset: 1640995200
Retry-After: 3600
```

## Rate Limit Responses

When a rate limit is exceeded, Flight Control returns:

- **HTTP Status**: `429 Too Many Requests`
- **Response Body**: JSON error message
- **Retry-After Header**: Time to wait before retrying

### Example Rate Limit Response

```json
{
  "apiVersion": "v1alpha1",
  "kind": "Status",
  "status": "Failure",
  "code": 429,
  "reason": "Too Many Requests",
  "message": "Rate limit exceeded. Please try again later."
}
```

## Testing Rate Limiting

### Verify Configuration

To verify that rate limiting is working correctly:

1. **Check headers**: Look for `X-RateLimit-Limit` and `X-RateLimit-Window` in API responses
2. **Test limits**: Make requests until you hit the rate limit
3. **Test different IPs**: Ensure different client IPs have separate limits

### Test Trusted Proxy Setup

1. **Check configuration loading**:

   ```bash
   # Look for trusted proxy configuration in logs
   grep -i "trusted.*proxy" /var/log/flightctl/flightctl-api.log
   ```

2. **Test with proxy headers**:

   ```bash
   # Test from trusted proxy IP
   curl -H "X-Forwarded-For: 203.0.113.100" \
        -H "Authorization: Bearer $TOKEN" \
        https://api.flightctl.example.com/api/v1/fleets
   
   # Test from untrusted IP (should ignore headers)
   curl -H "X-Forwarded-For: 203.0.113.100" \
        -H "Authorization: Bearer $TOKEN" \
        https://api.flightctl.example.com/api/v1/fleets
   ```

3. **Monitor rate limiting**:
   - Check that rate limits are applied per real client IP
   - Verify that proxy IPs don't affect rate limiting

## Troubleshooting

### Common Issues

#### All requests appear to come from the same IP

- **Cause**: Reverse proxy not configured to send real client IP
- **Solution**: Configure proxy headers as shown above

#### Rate limiting not working

- **Cause**: Rate limiting not enabled in configuration
- **Solution**: Add rate limit configuration to your config file

#### Unexpected rate limiting

- **Cause**: Rate limits too restrictive
- **Solution**: Adjust the `requests` and `window` values

#### All requests appear to come from the same IP (with trusted proxies)

**Cause**: Trusted proxies not configured or incorrect IP ranges

**Solution**:

1. Verify the proxy IP ranges in your configuration
2. Check network routing and container networking
3. Test with `curl` from different network locations

#### Rate limiting not working with proxy headers

**Cause**: Trusted proxies configuration is missing or empty

**Solution**:

1. Add `trustedProxies` configuration
2. Include the correct IP ranges for your infrastructure
3. Restart the Flight Control API service

#### Security concerns about IP ranges

**Cause**: Using overly broad IP ranges

**Solution**:

1. Use specific IP ranges rather than broad networks
2. Document which services are in each range
3. Regularly audit and update the configuration

### Debugging

Enable debug logging to see rate limiting decisions:

```yaml
service:
  logLevel: "debug"
```

Check logs for rate limiting information and the IP addresses being used. Look for log messages like:

```text
Trusted proxy check: IP 10.0.1.100 in trusted range 10.0.0.0/8
Using X-Forwarded-For header: 203.0.113.100
```

## Security Considerations

When using rate limiting with reverse proxies:

1. **Trust your proxy**: Only allow trusted reverse proxies to set IP headers
2. **Configure trusted proxies**: Set `trustedProxies` to include only your load balancer and UI proxy IPs
3. **Network security**: Restrict direct access to the Flight Control API
4. **Monitor usage**: Watch for unusual rate limiting patterns

### Trusted Proxy Configuration

The `trustedProxies` setting is **critical for security**. It ensures that only requests from trusted sources (like your load balancer or UI proxy) can set proxy headers like `X-Forwarded-For` or `X-Real-IP`.

#### Silent-Ignore Behavior

Flight Control implements a **silent-ignore** policy for untrusted proxy headers:

- **Trusted proxies**: Proxy headers from IPs in the `trustedProxies` list are processed normally and override the client IP
- **Untrusted sources**: Proxy headers from IPs not in the `trustedProxies` list are **silently ignored** - no error is returned, no logging occurs, and the request continues with the direct connection IP
- **Security benefit**: This prevents header spoofing attacks while maintaining service availability

**Example behavior**:

- Request from `10.0.1.100` (trusted) with `X-Forwarded-For: 192.168.1.50` → Client IP becomes `192.168.1.50`
- Request from `203.0.113.10` (untrusted) with `X-Forwarded-For: 192.168.1.50` → Client IP remains `203.0.113.10` (header ignored)

#### Security Best Practices

**1. Principle of Least Privilege:**

Only include the minimum necessary IP ranges:

```yaml
# Good: Specific ranges
trustedProxies:
  - "10.0.1.0/24"    # Load balancer subnet
  - "10.0.2.0/24"    # UI proxy subnet

# Avoid: Overly broad ranges
trustedProxies:
  - "0.0.0.0/0"      # Never use this!
```

**2. Network Segmentation:**

Use separate networks for different components:

```yaml
trustedProxies:
  - "10.0.1.0/24"    # Load balancer network
  - "10.0.2.0/24"    # Application network
  - "10.0.3.0/24"    # UI proxy network
```

**3. Regular Auditing**:

- Review trusted proxy configuration quarterly
- Remove unused IP ranges
- Document changes and reasons

#### Example Configuration for Common Deployments

**Kubernetes/OpenShift:**

```yaml
trustedProxies:
  - "10.0.0.0/8"     # Internal cluster network
  - "172.16.0.0/12"  # Pod network range
  - "192.168.0.0/16" # Service network range
```

**Docker/Podman:**

```yaml
trustedProxies:
  - "172.16.0.0/12"  # Docker bridge network
  - "127.0.0.1/32"   # Localhost (for development)
```

**Production with Load Balancer:**

```yaml
trustedProxies:
  - "203.0.113.0/24" # Load balancer IP range
  - "10.0.0.0/8"     # Internal application network
```

**Security Warning**: If `trustedProxies` is not configured or is empty, proxy headers will be ignored for security reasons. This means all requests will be rate-limited based on the direct connection IP, which may not be the real client IP when using reverse proxies.

For more security guidance, see [Security Guidelines](security-guidelines.md).

## Best Practices

1. **Start with conservative limits**: Begin with lower limits and adjust based on usage
2. **Monitor rate limiting**: Watch for legitimate users hitting limits
3. **Use appropriate windows**: Shorter windows for auth, longer for general API
4. **Test thoroughly**: Verify rate limiting works with your specific setup
5. **Document limits**: Make sure your users know about rate limits

## Examples

### Development Environment

```yaml
service:
  rateLimit:
    requests: 1000    # Higher limits for development
    window: "1m"
    authRequests: 100
    authWindow: "1h"
```

### Production Environment

```yaml
service:
  rateLimit:
    requests: 300     # Default production setting (5 requests/second)
    window: "1m"
    authRequests: 20  # Default production setting
    authWindow: "1h"
```

### High-Traffic Environment

```yaml
service:
  rateLimit:
    requests: 600     # Higher limits for high traffic (10 requests/second)
    window: "1m"
    authRequests: 50   # Higher auth limits for high traffic
    authWindow: "1h"
```
