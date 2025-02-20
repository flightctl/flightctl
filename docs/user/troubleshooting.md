# Troubleshooting

## Verifying the effective device specification received by the device agent

When viewing a device resource using the command

```console
flightctl get device/${device_name} -o yaml
```

the output contains the device specification as specified by the user or the fleet controller based on the fleet's device template. That specification may contain references to configuration or secrets stored on external systems, such a Git or a Kubernetes cluster.

Only when the device agent queries the service, the service replaces these references with the actual configuration and secret data. While this better protects potentially sensitive data, it also makes troubleshooting faulty configurations hard.

Users with the `GetRenderedDevice` permission can run the following command to view the effective configuration as rendered by the service to the device agent:

```console
flightctl get device/${device_name} -o yaml --rendered
```

## Generating a device log bundle

The device includes a script that generates a bundle of logs necessary to debug the agent. Run the command below on the device:

```console
sudo flightctl-must-gather
```

The output is a tarball named `must-gather-$timestamp.tgz`, whereby `$timestamp` is the current date and time. Include this tarball in the bug report.
