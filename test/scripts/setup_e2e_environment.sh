#!/usr/bin/env bash
set -euo pipefail

# Retain this script as the Make target's setup hook, but do not copy the QCOW
# into the libvirt images directory. The VM pool uses the prepared project disk
# directly and creates its own shared base disk under its temporary directory.
echo "E2E VM setup uses the prepared project QCOW directly; no libvirt image staging is needed."
