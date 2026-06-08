# Viewing vulnerabilities

When vulnerability integration is enabled, Flight Control periodically scans the OS images running on your devices and stores vulnerability findings. You can view this data through the CLI or API to understand your fleet's security posture.

For information on enabling vulnerability integration, see [Configuring Vulnerability Integration](../installing/configuring-vulnerability-integration.md).

## Viewing the vulnerability summary

To get a high-level overview of vulnerabilities across your entire fleet, use the summary command:

```console
flightctl get vuln --summary-only
```

The output shows the total count of vulnerabilities grouped by severity:

```console
CRITICAL  HIGH  MEDIUM  LOW  UNKNOWN  TOTAL
3         12    45      28   2        90
```

## Listing all vulnerabilities

To list all known vulnerabilities affecting devices in your fleet:

```console
flightctl get vuln
```

The output shows each CVE with its severity and score:

```console
CVE ID            SEVERITY  CVSS SCORE  PUBLISHED
CVE-2023-44487    High      7.5         2023-10-10
CVE-2023-38545    Critical  9.8         2023-10-11
CVE-2023-4911     High      7.8         2023-10-03
```

You can sort the results by different fields:

```console
# Sort by CVSS score, highest first
flightctl get vuln --sort-by cvssScore --order desc

# Sort by publication date, newest first
flightctl get vuln --sort-by publishedAt --order desc
```

## Viewing device vulnerabilities

To see vulnerabilities affecting a specific device:

```console
flightctl get vuln device/my-device
```

To include a summary before the vulnerability list:

```console
flightctl get vuln device/my-device --summary
```

```console
CRITICAL  HIGH  MEDIUM  LOW  UNKNOWN  TOTAL
1         3     8       5    0        17

CVE ID            SEVERITY  CVSS SCORE  PUBLISHED
CVE-2023-38545    Critical  9.8         2023-10-11
CVE-2023-44487    High      7.5         2023-10-10
...
```

To show only the summary without the full list:

```console
flightctl get vuln device/my-device --summary-only
```

## Viewing fleet vulnerabilities

To see vulnerabilities affecting devices in a specific fleet:

```console
flightctl get vuln fleet/production
```

Fleet vulnerability queries aggregate findings across all devices in the fleet. You can use the same `--summary` and `--summary-only` flags:

```console
# Show fleet summary with vulnerability list
flightctl get vuln fleet/production --summary

# Show only fleet summary
flightctl get vuln fleet/production --summary-only
```

## Viewing CVE impact

To understand the blast radius of a specific CVE, use the impact command:

```console
flightctl get vuln CVE-2023-44487
```

The output shows detailed information about the CVE and lists affected fleets and devices:

```console
CVE ID      CVE-2023-44487
SEVERITY    High (7.5)
ADVISORY    RHSA-2023:5838
ISSUER      Red Hat
LINK        https://access.redhat.com/security/cve/CVE-2023-44487
DESCRIPTION
HTTP/2 Rapid Reset Attack allows denial of service through rapid stream
creation and cancellation, overwhelming server resources.

AFFECTED FLEETS     AFFECTED DEVICES
production          12
staging             5
development         3
```

The `link` field provides a direct URL to the CVE details. For Red Hat advisories, the link points to the Red Hat Security portal. For other issuers, it points to the NVD database.

## Finding devices affected by a CVE

To list all devices affected by a specific CVE:

```console
flightctl get devices --cve-id CVE-2023-44487
```

You can combine this with other filters to narrow the results:

```console
# Find affected devices in a specific region
flightctl get devices --cve-id CVE-2023-44487 --selector region=us-west

# Find fleetless devices affected by a CVE
flightctl get devices --cve-id CVE-2023-44487 --field-selector "metadata.owner notcontains Fleet/"

# Show affected devices with labels
flightctl get devices --cve-id CVE-2023-44487 -o wide
```

## Output formats

All vulnerability commands support multiple output formats:

```console
# JSON output
flightctl get vuln -o json

# YAML output
flightctl get vuln device/my-device -o yaml

# Wide table output (additional columns)
flightctl get vuln fleet/production -o wide
```

## CVE events

High-severity findings trigger per-device [Events](../references/events.md#vulnerability-cve-events) after each vulnerability sync. Use the CLI to inspect them:

```console
flightctl get events --field-selector="reason in (DeviceVulnerabilityCVEWarning,DeviceVulnerabilityCVECritical,DeviceVulnerabilityCVEResolved)"
```

## Pagination

For large result sets, use pagination:

```console
# Get first 10 vulnerabilities
flightctl get vuln --limit 10

# Get next page using continue token from previous response
flightctl get vuln --limit 10 --continue <token>
```

## Troubleshooting

### No vulnerability data appears

If vulnerability queries return empty results:

1. Verify vulnerability integration is enabled. Check that `vulnerabilityReporting.enabled` is `true` in your service configuration.

2. Check that devices have reported an image digest. Run `flightctl get device/<name> -o yaml` and verify `status.os.imageDigest` is populated.

3. Verify Trustify has ingested data. The Trustify instance must have SBOM or advisory data for the image digests in use.

4. Check sync status. Review the periodic service logs for lines containing `vulnerability-sync`.

### Feature disabled error

If you receive a `501 Not Implemented` error with `reason: FeatureDisabled`, vulnerability integration is not enabled. See [Configuring Vulnerability Integration](../installing/configuring-vulnerability-integration.md) to enable it.
