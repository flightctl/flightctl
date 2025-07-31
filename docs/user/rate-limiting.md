# Rate Limiting

Flight Control implements rate limiting to protect the API from abuse and ensure fair usage. This document explains how rate limiting works and how to configure it properly.

## Overview

Rate limiting in Flight Control is **IP-based**, meaning requests are limited per client IP address. This helps prevent abuse while allowing legitimate users to access the API normally.

## How It Works

### IP Detection

Flight Control automatically detects the real client IP address using the following priority order:

1. **True-Client-IP header**
2. **X-Real-IP header**
3. **X-Forwarded-For header** (most common with reverse proxies)
4. **Direct connection IP** (fallback)

### Rate Limit Types

Flight Control applies different rate limits for different types of requests:

- **General API requests**: Higher limits for normal API operations
- **Authentication requests**: Stricter limits for login/validation endpoints

## Configuration

Rate limiting is configured in your Flight Control configuration file:

```yaml
service:
  rateLimit:
    # General API rate limiting
    requests: 60        # Maximum requests per window
    window: "1m"        # Time window (e.g., "1m", "1h", "1d")
    
    # Authentication-specific rate limiting (stricter)
    authRequests: 10    # Maximum auth requests per window
    authWindow: "1h"    # Auth time window
    
    # Trusted proxies that can set True-Client-IP/X-Forwarded-For/X-Real-IP headers
    # This should include your load balancer and UI proxy IPs
    trustedProxies:
      - "10.0.0.0/8"     # Internal network range
      - "172.16.0.0/12"  # Docker/container network range
      - "192.168.0.0/16" # Private network range
```

### Default Values

If not configured, rate limiting is **disabled by default**. To enable it, you must explicitly set the configuration values.

**Important**: When using reverse proxies (load balancers, API gateways, etc.), you **must** configure `trustedProxies` to include the IP ranges of your proxy infrastructure. Without this configuration, proxy headers will be ignored for security reasons, and all requests will be rate-limited based on the proxy's IP address rather than the real client IP.

### Environment Variables

You can also configure rate limiting using environment variables:

```bash
# General rate limiting
export RATE_LIMIT_REQUESTS=60
export RATE_LIMIT_WINDOW=1m

# Auth-specific rate limiting
export AUTH_RATE_LIMIT_REQUESTS=10
export AUTH_RATE_LIMIT_WINDOW=1h

# Trusted proxies (comma-separated list)
export RATE_LIMIT_TRUSTED_PROXIES="10.0.0.0/8,172.16.0.0/12,192.168.0.0/16"
```

**Important**: Environment variables **override** settings in the configuration file. The configuration loading process works as follows:

1. **Config file is loaded first** with default values
2. **Environment variables are processed second** and override any corresponding config file settings
3. **Only non-empty environment variables** override config file values

**Note**: The `RATE_LIMIT_TRUSTED_PROXIES` environment variable accepts a comma-separated list of CIDR ranges. Each CIDR should be in standard format (e.g., `10.0.0.0/8`, `172.16.0.0/12`).

**Example**: If your config file has `requests: 100` but you set `RATE_LIMIT_REQUESTS=60`, the final value will be `60` (the environment variable takes precedence).

## Reverse Proxy Configuration

When using a reverse proxy (nginx, HAProxy, load balancer, etc.), you have two options for proper rate limiting:

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
    requests: 60      # Conservative limits for production
    window: "1m"
    authRequests: 10  # Stricter auth limits
    authWindow: "1h"
```

### High-Traffic Environment

```yaml
service:
  rateLimit:
    requests: 200     # Higher limits for high traffic
    window: "1m"
    authRequests: 20
    authWindow: "1h"
```
