# services sub-package
%package services
Summary: Flight Control services
Requires: bash
Requires: podman
Requires: python3-pyyaml
BuildRequires: systemd-rpm-macros
%{?systemd_requires}

%description services
The flightctl-services package provides installation and setup of files for running containerized Flight Control services

%install
install -Dpm 0644 packaging/flightctl-services-install.conf %{buildroot}%{_sysconfdir}/flightctl/flightctl-services-install.conf
CONFIG_READONLY_DIR="%{buildroot}%{_datadir}/flightctl" CONFIG_WRITEABLE_DIR="%{buildroot}%{_sysconfdir}/flightctl" QUADLET_FILES_OUTPUT_DIR="%{buildroot}%{_datadir}/containers/systemd" SYSTEMD_UNIT_OUTPUT_DIR="%{buildroot}/usr/lib/systemd/system" IMAGE_TAG=$(echo %{version} | tr '~' '-') deploy/scripts/install.sh

%files services
    %defattr(0644,root,root,-)
    # Files mounted to system config
    %dir %{_sysconfdir}/flightctl
    %dir %{_sysconfdir}/flightctl/pki
    %dir %{_sysconfdir}/flightctl/flightctl-api
    %dir %{_sysconfdir}/flightctl/flightctl-ui
    %dir %{_sysconfdir}/flightctl/flightctl-cli-artifacts
    %dir %{_sysconfdir}/flightctl/flightctl-alertmanager-proxy
    %dir %{_sysconfdir}/flightctl/ssh
    %config(noreplace) %{_sysconfdir}/flightctl/service-config.yaml
    %config(noreplace) %{_sysconfdir}/flightctl/flightctl-services-install.conf
    %config(noreplace) %{_sysconfdir}/flightctl/ssh/known_hosts

    # Files mounted to data dir
    %dir %attr(0755,root,root) %{_datadir}/flightctl
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-api
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-alert-exporter
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-db
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-kv
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-alertmanager-proxy
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-ui
    %dir %attr(0755,root,root) %{_datadir}/flightctl/flightctl-cli-artifacts
    %{_datadir}/flightctl/flightctl-api/config.yaml.template
    %{_datadir}/flightctl/flightctl-api/env.template
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-api/init.sh
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-api/create_aap_application.sh
    %{_datadir}/flightctl/flightctl-alert-exporter/config.yaml
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-db/enable-superuser.sh
    %{_datadir}/flightctl/flightctl-kv/redis.conf
    %{_datadir}/flightctl/flightctl-ui/env.template
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-ui/init.sh
    %attr(0755,root,root) %{_datadir}/flightctl/init_utils.sh
    %{_datadir}/flightctl/flightctl-cli-artifacts/env.template
    %{_datadir}/flightctl/flightctl-cli-artifacts/nginx.conf
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-cli-artifacts/init.sh
    %{_datadir}/containers/systemd/flightctl*
    %{_datadir}/flightctl/flightctl-alertmanager/alertmanager.yml
    %{_datadir}/flightctl/flightctl-alertmanager-proxy/env.template
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-alertmanager-proxy/init.sh

    # Handle permissions for scripts setting host config
    %attr(0755,root,root) %{_datadir}/flightctl/init_host.sh
    %attr(0755,root,root) %{_datadir}/flightctl/secrets.sh
    %attr(0755,root,root) %{_datadir}/flightctl/yaml_helpers.py

    # flightctl-services pre upgrade checks
    %dir %{_libexecdir}/flightctl
    %attr(0755,root,root) %{_libexecdir}/flightctl/pre-upgrade-dry-run.sh

    # Files mounted to lib dir
    /usr/lib/systemd/system/flightctl.target

# Optional pre-upgrade database migration dry-run
%pre services
  # $1 == 1 if it's an install
  # $1 == 2 if it's an upgrade
  if [ "$1" -eq 2 ]; then
      IMAGE_TAG="$(echo %{version} | tr '~' '-')"
      echo "flightctl: running pre upgrade checks, target version $IMAGE_TAG"
      if [ -x "%{_libexecdir}/flightctl/pre-upgrade-dry-run.sh" ]; then
          IMAGE_TAG="$IMAGE_TAG" \
          CONFIG_PATH="%{_sysconfdir}/flightctl/flightctl-api/config.yaml" \
          "%{_libexecdir}/flightctl/pre-upgrade-dry-run.sh" "$IMAGE_TAG" "%{_sysconfdir}/flightctl/flightctl-api/config.yaml" || {
              echo "flightctl: dry-run failed; aborting upgrade." >&2
              exit 1
          }
      else
          echo "flightctl: pre-upgrade-dry-run.sh not found at %{_libexecdir}/flightctl; skipping."
      fi
  fi

%post services
  # On initial install: apply preset policy to enable/disable services based on system defaults
  %systemd_post %{flightctl_target}

  # Reload systemd to recognize new container files
  /usr/bin/systemctl daemon-reload >/dev/null 2>&1 || :

  cfg="%{_sysconfdir}/flightctl/flightctl-services-install.conf"

  if [ "$1" -eq 1 ]; then # it's a fresh install
    %{__cat} <<EOF
[flightctl] Installed.

Start services:
  sudo systemctl start flightctl.target

Check status:
  systemctl list-units 'flightctl*' --all
EOF
fi

# Suggest enabling migration dry-run if not set
if [ -f "$cfg" ] && ! %{__grep} -q '^[[:space:]]*FLIGHTCTL_MIGRATION_DRY_RUN=1[[:space:]]*$' "$cfg"; then
  %{__cat} <<EOF
Recommendation:
  A database migration dry-run before updates is currently DISABLED.
  To enable it, edit:
    $cfg
  and set:
    FLIGHTCTL_MIGRATION_DRY_RUN=1
EOF
fi

if [ "$1" -eq 2 ]; then # it's an upgrade
  %{__cat} <<'EOF'
[flightctl] Upgraded.

Review status:
  systemctl list-units 'flightctl*' --all
EOF
  fi

%preun services
  # On package removal: stop and disable all services
  %systemd_preun %{flightctl_target}
  %systemd_preun flightctl-network.service

%postun services
  # On upgrade: mark services for restart after transaction completes
  %systemd_postun_with_restart %{flightctl_services_restart}
  %systemd_postun %{flightctl_target}
