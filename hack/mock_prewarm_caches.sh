#!/usr/bin/env bash
set -euo pipefail

for r in centos-stream+epel-next-9-x86_64 epel-10-x86_64; do
  echo "Initializing mock root $r"
  mock -r "$r" --enable-network --init
  # drop full chroot to keep image smaller, keep only caches
  rm -rf "/var/lib/mock/$r/root" "/var/lib/mock/$r/result"
 done
