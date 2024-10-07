#!/bin/env bash
set -e -x -o pipefail

FLIGHTCTL_NS=${FLIGHTCTL_NS:-flightctl-external}

mkdir -p  ~/.flightctl/certs

 # Extract .fligthctl files from the api pod, but we must wait for the server to be ready

kubectl rollout status deployment flightctl-api -n "${FLIGHTCTL_NS}" -w --timeout=300s

  # we actually don't need to download the ca.key or server.key but apparently the flightctl
  # client expects them to be present TODO: fix this in flightctl
  API_POD=$(kubectl get pod -n "${FLIGHTCTL_NS}" -l flightctl.service=flightctl-api --no-headers -o custom-columns=":metadata.name" | head -1 )

  # wait for the server to write the client-enrollment.key
  until kubectl exec -n "${FLIGHTCTL_NS}" "${API_POD}" -- cat /root/.flightctl/certs/client-enrollment.key > /dev/null 2>&1;
  do
    sleep 1;
  done

  # pull agent-usable details as well as client configuration file
  for f in certs/{ca.crt,client-enrollment.crt,client-enrollment.key}; do
    # a kubectl cp would be more efficient, but tar is not available on the image, and we don't want
    # to switch from ubi9-micro just for tar
    kubectl exec -n "${FLIGHTCTL_NS}" "${API_POD}" -- cat /root/.flightctl/$f > ~/.flightctl/$f
  done

  chmod og-rwx ~/.flightctl/certs/*.key