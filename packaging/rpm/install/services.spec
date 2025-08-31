# flightctl-services sub-package steps
# Run the install script to move the quadlet files.
#
# The IMAGE_TAG is derived from the RPM version, which may include tildes (~)
# for proper version sorting (e.g., 0.5.1~rc1-1). However, the tagged images
# always use hyphens (-) instead of tildes (~). To ensure valid image tags we need
# to transform the version string by replacing tildes with hyphens.
CONFIG_READONLY_DIR="%{buildroot}%{_datadir}/flightctl" \
CONFIG_WRITEABLE_DIR="%{buildroot}%{_sysconfdir}/flightctl" \
QUADLET_FILES_OUTPUT_DIR="%{buildroot}%{_datadir}/containers/systemd" \
SYSTEMD_UNIT_OUTPUT_DIR="%{buildroot}/usr/lib/systemd/system" \
IMAGE_TAG=$(echo %{version} | tr '~' '-') \
deploy/scripts/install.sh
