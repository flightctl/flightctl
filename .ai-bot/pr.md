## Summary
- Add RHEL-specific bootc-image-builder (BIB) configuration to fix ImageExport failures on RHEL 10 bootc images
- Auto-detect RHEL 10 source images and use `registry.redhat.io/rhel10/bootc-image-builder:latest` instead of the default centos-bootc BIB
- Add `rhelBootcImageBuilder` service image config to Helm chart, Podman quadlet templates, and service config

## Test plan
- [x] Unit tests for RHEL 10 image detection (`isRHEL10Image`) with 8 test cases
- [x] Unit tests for RHEL BIB config accessors (`EffectiveRhelBootcImageBuilderImage`) with 4 test cases
- [x] Unit test verifying default config includes RHEL BIB
- [x] All existing unit tests pass
- [x] golangci-lint passes with no issues
- [ ] Manual: Deploy with RHEL 10 bootc source image, verify ImageExport uses RHEL BIB and succeeds
- [ ] Manual: Deploy with RHEL 9 bootc source image, verify ImageExport still uses default BIB
