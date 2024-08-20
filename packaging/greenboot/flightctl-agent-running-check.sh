#!/usr/bin/env bash

set -x -eo pipefail

TIMEOUT=60
SUCCESS_DURATION=60

# This script is used to check if the flightctl-agent service is running. It is
# used in the greenboot process to ensure that the flightctl-agent service is
# running before proceeding. The script will wait for the service to be active,
# and will fail if the service is not active within the timeout.

verify_flightctl_agent_status() {
  local -r failed_status=$(systemctl is-failed flightctl-agent.service)
  local -r active_status=$(systemctl is-active flightctl-agent.service)

  # fail fast
  if [ "${failed_status}" = "failed" ] ; then
    echo "Critical: flightctl-agent.service has encountered a failure. Aborting..."
    kill -TERM ${SCRIPT_PID}
  fi

  if ! [ "${active_status}" = "active" ] ; then
    return 1
  fi

  return 0
}

wait_for_success() {
  local timeout=$1
  local success_duration=$2
  shift 2
  local command="$@"

  local -r start=$(date +%s)
  local now

  while : ; do
    $command & # run in background
    local command_pid=$!

    while : ; do
      # verify command still running
      if kill -0 $command_pid 2>/dev/null; then
        now=$(date +%s)
        if [ $((now - start)) -ge "${timeout}" ]; then
          # timeout fail fast
          kill $command_pid
          return 1
        fi
        sleep 1
      else
        # check status of command and return it
        wait $command_pid
        local status=$?
        if [ $status -eq 0 ]; then
          # success, now ensure runtime for success_duration
          local -r success_start=$(date +%s)
          local success_now
          while : ; do
            # new check command
            $command &
            local check_pid=$!
            while kill -0 $check_pid 2>/dev/null; do
              success_now=$(date +%s)
              if [ $((success_now - success_start)) -ge "${success_duration}" ]; then
                kill $check_pid
                return 0
              fi
              sleep 1
            done
            wait $check_pid
            local check_status=$?
            if [ $check_status -ne 0 ]; then
              return 1
            fi
          done
        else
          # retry until timeout
          break
        fi
      fi
    done

    now=$(date +%s)
    if [ $((now - start)) -ge "${timeout}" ]; then
      return 1
    fi
  done
}


# Wait for flightctl-agent service to be active (failed status terminates the script)
echo "Waiting ${TIMEOUT}s for flightctl-agent service to be active..."
if ! wait_for_success "${TIMEOUT}" "${SUCCESS_DURATION}" verify_flightctl_agent_status ; then
  echo "Error: flightctl-agent.service did not become active within ${TIMEOUT}s. Aborting..."
  exit 1
fi
