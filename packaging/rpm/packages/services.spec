# services sub-package
%package services
Summary: Flight Control services
Requires: bash
Requires: podman

%description services
The flightctl-services package provides installation and setup of files for running containerized Flight Control services

%files services
    %defattr(0644,root,root,-)
    # Files mounted to system config
    %dir %{_sysconfdir}/flightctl
    %dir %{_sysconfdir}/flightctl/pki
    %dir %{_sysconfdir}/flightctl/flightctl-api
    %dir %{_sysconfdir}/flightctl/flightctl-ui
    %dir %{_sysconfdir}/flightctl/flightctl-cli-artifacts
    %dir %{_sysconfdir}/flightctl/flightctl-alertmanager-proxy
    %config(noreplace) %{_sysconfdir}/flightctl/service-config.yaml

    # Files mounted to data dir
    %dir %attr(0444,root,root) %{_datadir}/flightctl
    %dir %attr(0444,root,root) %{_datadir}/flightctl/flightctl-api
    %dir %attr(0444,root,root) %{_datadir}/flightctl/flightctl-alert-exporter
    %dir %attr(0444,root,root) %{_datadir}/flightctl/flightctl-db
    %dir %attr(0444,root,root) %{_datadir}/flightctl/flightctl-db-migrate
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-db-migrate/migration-setup.sh
    %dir %attr(0444,root,root) %{_datadir}/flightctl/flightctl-kv
    %dir %attr(0444,root,root) %{_datadir}/flightctl/flightctl-ui
    %dir %attr(0444,root,root) %{_datadir}/flightctl/flightctl-cli-artifacts
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

    # Files mounted to lib dir
    /usr/lib/systemd/system/flightctl.target
    /usr/lib/systemd/system/flightctl-db-migrate.service
