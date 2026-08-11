## Summary

- Remove stray `Requires: openssl` from the main (empty) RPM package scope in `packaging/rpm/flightctl.spec` — this dependency was ineffective because the main package is never built
- The correct `Requires: openssl` in the `%package services` sub-package (added in EDM-4743) is already in place and ensures `openssl` is installed as a dependency of `flightctl-services`

## Test plan

- [ ] Verify `rpmlint packaging/rpm/flightctl.spec` passes without new warnings
- [ ] Verify `flightctl-services` RPM still lists `openssl` as a dependency (`rpm -qR flightctl-services | grep openssl`)
- [ ] Install `flightctl-services` on a minimal RHEL system and confirm `openssl` is pulled in automatically
- [ ] Start `flightctl-certs-init.service` and confirm certificate generation succeeds

🤖 Generated with [Claude Code](https://claude.com/claude-code)
