%define debug_package %{nil}

Name: flightctl
Version: 0.3.0
Release: 1.20241211164210325170.hooks.exec.115.gcae26e9d%{?dist}
Summary: Flightctl CLI

License: XXX
URL: https://github.com/flightctl/flightctl
Source0: flightctl-0.3.0.tar.gz

BuildRequires: golang
BuildRequires: make
BuildRequires: git
BuildRequires: openssl-devel
Requires: openssl

%description
Flightctl is a command line interface for managing edge device fleets.


%package agent
Summary: Flightctl Agent
%description agent
Flightctl Agent is a component of the flightctl tool.

%prep
%setup -q -n flightctl-0.3.0

%build

# if this is a buggy version of go we need to set GOPROXY as workaround
# see https://github.com/golang/go/issues/61928
GOENVFILE=$(go env GOROOT)/go.env
if [[ ! -f "{$GOENVFILE}" ]]; then
    export GOPROXY='https://proxy.golang.org,direct'
fi
make build

%install
mkdir -p %{buildroot}/usr/bin
cp bin/flightctl %{buildroot}/usr/bin
mkdir -p %{buildroot}/usr/lib/systemd/system
mkdir -p %{buildroot}/%{_sharedstatedir}/flightctl
mkdir -p %{buildroot}/usr/lib/flightctl/hooks.d/{afterupdating,beforeupdating,afterrebooting,beforerebooting}
mkdir -p %{buildroot}/usr/lib/greenboot/check/required.d
install -m 0755 packaging/greenboot/flightctl-agent-running-check.sh %{buildroot}/usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
cp bin/flightctl-agent %{buildroot}/usr/bin
cp packaging/must-gather/flightctl-must-gather %{buildroot}/usr/bin
cp packaging/hooks.d/afterupdating/00-default.yaml %{buildroot}/usr/lib/flightctl/hooks.d/afterupdating
cp packaging/systemd/flightctl-agent.service %{buildroot}/usr/lib/systemd/system
bin/flightctl completion bash > flightctl-completion.bash
install -Dpm 0644 flightctl-completion.bash -t %{buildroot}/%{_datadir}/bash-completion/completions/flightctl-completion.bash
bin/flightctl completion fish > flightctl-completion.fish
install -Dpm 0644 flightctl-completion.fish -t %{buildroot}/%{_datadir}/fish/vendor_completions.d/flightctl-completion.fish
bin/flightctl completion zsh > _flightctl-completion
install -Dpm 0644 _flightctl-completion -t %{buildroot}/%{_datadir}/zsh/site-functions/_flightctl-completion

%files
/usr/bin/flightctl
%{_datadir}/bash-completion/completions/flightctl-completion.bash
%{_datadir}/fish/vendor_completions.d/flightctl-completion.fish
%{_datadir}/zsh/site-functions/_flightctl-completion

%files agent
/usr/bin/flightctl-agent
/usr/bin/flightctl-must-gather
/usr/lib/flightctl/hooks.d/afterupdating/00-default.yaml
/usr/lib/systemd/system/flightctl-agent.service
%{_sharedstatedir}/flightctl
/usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh

%changelog
* Wed Dec 11 2024 Sam Batschelet <sbatsche@redhat.com> - 0.3.0-1.20241211164210325170.hooks.exec.115.gcae26e9d
- wip (Sam Batschelet)
- EDM-848: revert tzdata host mount (#690) (Sam Batschelet)
- EDM-876: Fix helm template error (Celia Amador)
- EDM-788: Add init script for PostgreSQL to set master user as superuser (#679) (Assaf Albo)
- NO-ISSUE: redeploy kind pods for development (Miguel Angel Ajo)
- NO-ISSUE: move ownership od status exporters to managers (Sam Batschelet)
- EDM-853: spec/queue: ensure item requeue backoff only if tried  (#696) (Sam Batschelet)
- EDM-642: Remove sorting mechanism for TP phase (#701) (Assaf Albo)
- MGMT-17672: Document device statuses (Frank A. Zdarsky)
- EDM-417: Get renderedVersion from DB (Frank A. Zdarsky)
- EDM-417: Update unit-tests for server-side status (Frank A. Zdarsky)
- EDM-417: Do not report retries, fail faster (Frank A. Zdarsky)
- EDM-417: Compute service-side status (Frank A. Zdarsky)
- EDM-649: Add specific error message when CSR approval fails (Lily Sturmann)
- EDM-649: Remove extraneous CN len check (Lily Sturmann)
- EDM-842: transient networking error should not mark agent degraded (Sam Batschelet)
- EDM-772: Move UI chart to main repo (#673) (Rastislav Wagner)
- NO-ISSUE: agent: ensure bootstrap status (Sam Batschelet)
- EDM-761: Document lifecycle hooks (Frank A. Zdarsky)
- EDM-764: Update Hooks API implementation (Frank A. Zdarsky)
- EDM-764: Update Hooks API (Frank A. Zdarsky)
- EDM-659: agent/fileio: ensure ignition gid, uid and mode (Sam Batschelet)
- NO-ISSUE: Pass internalOpenShiftApiUrl from values to api-config CM (#680) (Rastislav Wagner)
- EDM-424: add update policy api and validation (Sam Batschelet)
- EDM-420: Increase DB parameters (#669) (Gregory Shilin)
- NO-ISSUE: Append UI port to valid redirect URIs (rawagner)
- EDM-390: Add decommissioning route to device API (Lily Sturmann)
- EDM-417: Centralize API consts (Frank A. Zdarsky)
- NO-ISSUE: add command to prepare e2e test (sserafin)
- EDM-621: Fix microshift registration docs (Avishay Traeger)
- EDM-767: Actually update ignition mount path (Avishay Traeger)
- EDM-708: Reduce git access (Avishay Traeger)
- NO-ISSUE: Remove duplicated code in git helpers (Avishay Traeger)
- MGMT-17672: device: align update status phases (Sam Batschelet)
- Add developer documentation for field-selector (#658) (Assaf Albo)
- EDM-681: Allow field selector when 'summaryOnly' is enabled (#656) (Assaf Albo)
- EDM-704: Automate test 75991 (sserafin)
- EDM-695: typo fix (Gregory Shilin)
- EDM-695: typo fix (Gregory Shilin)
- EDM-695: typo fix (Gregory Shilin)
- EDM-695: typo fix (Gregory Shilin)
- EDM-695: created a document with instructions to run devicesimulator (Gregory Shilin)
- EDM-695: Logger verbosity flag was added (-v) (#651) (Gregory Shilin)
- EDM-423: Fix lexer state for nested exists/notexists selectors (#655) (Assaf Albo)
- EDM-423: Adapt K8s lexer to support RHS equality symbol (asafbss)
- EDM-423: Modify k8s containment operator; remove support for 'in' and 'notin' on JSON fields (asafbss)
- EDM-423: Remove selector casting; implement a whitelist of selectors per resource for stricter filtering (asafbss)
- EDM-423: Move alias back to label in API while keeping it as a field in device model (asafbss)
- EDM-423: Add documentation for Field Selectors (asafbss)
- EDM-666: Updated selector names to match the documented API (asafbss)
- EDM-664: Added selector support for Spec field in field-selector (asafbss)
- EDM-423: Introduce Alias Field for Device Metadata (asafbss)
- EDM-423: Add more Kubernetes operators to enhance selectors for fields (asafbss)
- EDM-423: Kubernetes vanilla label selector (asafbss)
- NO-ISSUE: Fix linter and spellchecker issues (Frank A. Zdarsky)
- NO-ISSUE: Fix path-filter (Frank A. Zdarsky)
- EDM-694: Add Valkey key value store service (Ricardo Noriega)
- EDM-694: Use primary IP and nip.io domains (Ricardo Noriega)
- EDM-694: Add FlightCtl Network to slice (Ricardo Noriega)
- EDM-694: Add Makefile Quadlet targets for deploy and clean (Ricardo Noriega)
- EDM-694: Initial FlightCtl deployment with Quadlets (Ricardo Noriega)
- NO-ISSUE: Update config storage API (Avishay Traeger)
- EDM-578: Remove templateversion populate task (Avishay Traeger)
- EDM-582: Freeze HTTP configurations (Avishay Traeger)
- EDM-583: Freeze k8s secret configurations (Avishay Traeger)
- EDM-581: Freeze git configurations (Avishay Traeger)
- NO-ISSUE: Remove device's TV annotation if removed from fleet (Avishay Traeger)
- EDM-580: Deploy Valkey for frozen configurations (Avishay Traeger)
- EDM-578: Separate fleet validate and device render logic (Avishay Traeger)
- EDM-420: Enable to run devicesimulator on multiple hosts (#650) (Gregory Shilin)
- NO-ISSUE: Improve UpdateDeviceWithRetries to handle all errors (Miguel Angel Ajo)
- EDM-446: device/fileio: ensure readable paths in PathExists (Sam Batschelet)
- EDM-656: agent: clarify completed status (Sam Batschelet)
- EDM-656: agent: ensure Shutdown is threadsafe (Sam Batschelet)
- EDM-656: agent/device improve systemd status reporting (Sam Batschelet)
- NO-ISSUE: console: simplify exec test case (Sam Batschelet)
- EDM-673: Enable exposing APIs via Gateway (rawagner)
- EDM-650: Load k8s ca.crt when deployed on k8s (rawagner)

* Wed Dec 11 2024 Sam Batschelet <sbatsche@redhat.com> - 0.3.0-1.20241211162125454589.hooks.exec.115.gcae26e9d
- wip (Sam Batschelet)
- EDM-848: revert tzdata host mount (#690) (Sam Batschelet)
- EDM-876: Fix helm template error (Celia Amador)
- EDM-788: Add init script for PostgreSQL to set master user as superuser (#679) (Assaf Albo)
- NO-ISSUE: redeploy kind pods for development (Miguel Angel Ajo)
- NO-ISSUE: move ownership od status exporters to managers (Sam Batschelet)
- EDM-853: spec/queue: ensure item requeue backoff only if tried  (#696) (Sam Batschelet)
- EDM-642: Remove sorting mechanism for TP phase (#701) (Assaf Albo)
- MGMT-17672: Document device statuses (Frank A. Zdarsky)
- EDM-417: Get renderedVersion from DB (Frank A. Zdarsky)
- EDM-417: Update unit-tests for server-side status (Frank A. Zdarsky)
- EDM-417: Do not report retries, fail faster (Frank A. Zdarsky)
- EDM-417: Compute service-side status (Frank A. Zdarsky)
- EDM-649: Add specific error message when CSR approval fails (Lily Sturmann)
- EDM-649: Remove extraneous CN len check (Lily Sturmann)
- EDM-842: transient networking error should not mark agent degraded (Sam Batschelet)
- EDM-772: Move UI chart to main repo (#673) (Rastislav Wagner)
- NO-ISSUE: agent: ensure bootstrap status (Sam Batschelet)
- EDM-761: Document lifecycle hooks (Frank A. Zdarsky)
- EDM-764: Update Hooks API implementation (Frank A. Zdarsky)
- EDM-764: Update Hooks API (Frank A. Zdarsky)
- EDM-659: agent/fileio: ensure ignition gid, uid and mode (Sam Batschelet)
- NO-ISSUE: Pass internalOpenShiftApiUrl from values to api-config CM (#680) (Rastislav Wagner)
- EDM-424: add update policy api and validation (Sam Batschelet)
- EDM-420: Increase DB parameters (#669) (Gregory Shilin)
- NO-ISSUE: Append UI port to valid redirect URIs (rawagner)
- EDM-390: Add decommissioning route to device API (Lily Sturmann)
- EDM-417: Centralize API consts (Frank A. Zdarsky)
- NO-ISSUE: add command to prepare e2e test (sserafin)
- EDM-621: Fix microshift registration docs (Avishay Traeger)
- EDM-767: Actually update ignition mount path (Avishay Traeger)
- EDM-708: Reduce git access (Avishay Traeger)
- NO-ISSUE: Remove duplicated code in git helpers (Avishay Traeger)
- MGMT-17672: device: align update status phases (Sam Batschelet)
- Add developer documentation for field-selector (#658) (Assaf Albo)
- EDM-681: Allow field selector when 'summaryOnly' is enabled (#656) (Assaf Albo)
- EDM-704: Automate test 75991 (sserafin)
- EDM-695: typo fix (Gregory Shilin)
- EDM-695: typo fix (Gregory Shilin)
- EDM-695: typo fix (Gregory Shilin)
- EDM-695: typo fix (Gregory Shilin)
- EDM-695: created a document with instructions to run devicesimulator (Gregory Shilin)
- EDM-695: Logger verbosity flag was added (-v) (#651) (Gregory Shilin)
- EDM-423: Fix lexer state for nested exists/notexists selectors (#655) (Assaf Albo)
- EDM-423: Adapt K8s lexer to support RHS equality symbol (asafbss)
- EDM-423: Modify k8s containment operator; remove support for 'in' and 'notin' on JSON fields (asafbss)
- EDM-423: Remove selector casting; implement a whitelist of selectors per resource for stricter filtering (asafbss)
- EDM-423: Move alias back to label in API while keeping it as a field in device model (asafbss)
- EDM-423: Add documentation for Field Selectors (asafbss)
- EDM-666: Updated selector names to match the documented API (asafbss)
- EDM-664: Added selector support for Spec field in field-selector (asafbss)
- EDM-423: Introduce Alias Field for Device Metadata (asafbss)
- EDM-423: Add more Kubernetes operators to enhance selectors for fields (asafbss)
- EDM-423: Kubernetes vanilla label selector (asafbss)
- NO-ISSUE: Fix linter and spellchecker issues (Frank A. Zdarsky)
- NO-ISSUE: Fix path-filter (Frank A. Zdarsky)
- EDM-694: Add Valkey key value store service (Ricardo Noriega)
- EDM-694: Use primary IP and nip.io domains (Ricardo Noriega)
- EDM-694: Add FlightCtl Network to slice (Ricardo Noriega)
- EDM-694: Add Makefile Quadlet targets for deploy and clean (Ricardo Noriega)
- EDM-694: Initial FlightCtl deployment with Quadlets (Ricardo Noriega)
- NO-ISSUE: Update config storage API (Avishay Traeger)
- EDM-578: Remove templateversion populate task (Avishay Traeger)
- EDM-582: Freeze HTTP configurations (Avishay Traeger)
- EDM-583: Freeze k8s secret configurations (Avishay Traeger)
- EDM-581: Freeze git configurations (Avishay Traeger)
- NO-ISSUE: Remove device's TV annotation if removed from fleet (Avishay Traeger)
- EDM-580: Deploy Valkey for frozen configurations (Avishay Traeger)
- EDM-578: Separate fleet validate and device render logic (Avishay Traeger)
- EDM-420: Enable to run devicesimulator on multiple hosts (#650) (Gregory Shilin)
- NO-ISSUE: Improve UpdateDeviceWithRetries to handle all errors (Miguel Angel Ajo)
- EDM-446: device/fileio: ensure readable paths in PathExists (Sam Batschelet)
- EDM-656: agent: clarify completed status (Sam Batschelet)
- EDM-656: agent: ensure Shutdown is threadsafe (Sam Batschelet)
- EDM-656: agent/device improve systemd status reporting (Sam Batschelet)
- NO-ISSUE: console: simplify exec test case (Sam Batschelet)
- EDM-673: Enable exposing APIs via Gateway (rawagner)
- EDM-650: Load k8s ca.crt when deployed on k8s (rawagner)

* Wed Dec 11 2024 Sam Batschelet <sbatsche@redhat.com> - 0.3.0-1.20241211145402951653.hooks.exec.115.gcae26e9d
- wip (Sam Batschelet)
- EDM-848: revert tzdata host mount (#690) (Sam Batschelet)
- EDM-876: Fix helm template error (Celia Amador)
- EDM-788: Add init script for PostgreSQL to set master user as superuser (#679) (Assaf Albo)
- NO-ISSUE: redeploy kind pods for development (Miguel Angel Ajo)
- NO-ISSUE: move ownership od status exporters to managers (Sam Batschelet)
- EDM-853: spec/queue: ensure item requeue backoff only if tried  (#696) (Sam Batschelet)
- EDM-642: Remove sorting mechanism for TP phase (#701) (Assaf Albo)
- MGMT-17672: Document device statuses (Frank A. Zdarsky)
- EDM-417: Get renderedVersion from DB (Frank A. Zdarsky)
- EDM-417: Update unit-tests for server-side status (Frank A. Zdarsky)
- EDM-417: Do not report retries, fail faster (Frank A. Zdarsky)
- EDM-417: Compute service-side status (Frank A. Zdarsky)
- EDM-649: Add specific error message when CSR approval fails (Lily Sturmann)
- EDM-649: Remove extraneous CN len check (Lily Sturmann)
- EDM-842: transient networking error should not mark agent degraded (Sam Batschelet)
- EDM-772: Move UI chart to main repo (#673) (Rastislav Wagner)
- NO-ISSUE: agent: ensure bootstrap status (Sam Batschelet)
- EDM-761: Document lifecycle hooks (Frank A. Zdarsky)
- EDM-764: Update Hooks API implementation (Frank A. Zdarsky)
- EDM-764: Update Hooks API (Frank A. Zdarsky)
- EDM-659: agent/fileio: ensure ignition gid, uid and mode (Sam Batschelet)
- NO-ISSUE: Pass internalOpenShiftApiUrl from values to api-config CM (#680) (Rastislav Wagner)
- EDM-424: add update policy api and validation (Sam Batschelet)
- EDM-420: Increase DB parameters (#669) (Gregory Shilin)
- NO-ISSUE: Append UI port to valid redirect URIs (rawagner)
- EDM-390: Add decommissioning route to device API (Lily Sturmann)
- EDM-417: Centralize API consts (Frank A. Zdarsky)
- NO-ISSUE: add command to prepare e2e test (sserafin)
- EDM-621: Fix microshift registration docs (Avishay Traeger)
- EDM-767: Actually update ignition mount path (Avishay Traeger)
- EDM-708: Reduce git access (Avishay Traeger)
- NO-ISSUE: Remove duplicated code in git helpers (Avishay Traeger)
- MGMT-17672: device: align update status phases (Sam Batschelet)
- Add developer documentation for field-selector (#658) (Assaf Albo)
- EDM-681: Allow field selector when 'summaryOnly' is enabled (#656) (Assaf Albo)
- EDM-704: Automate test 75991 (sserafin)
- EDM-695: typo fix (Gregory Shilin)
- EDM-695: typo fix (Gregory Shilin)
- EDM-695: typo fix (Gregory Shilin)
- EDM-695: typo fix (Gregory Shilin)
- EDM-695: created a document with instructions to run devicesimulator (Gregory Shilin)
- EDM-695: Logger verbosity flag was added (-v) (#651) (Gregory Shilin)
- EDM-423: Fix lexer state for nested exists/notexists selectors (#655) (Assaf Albo)
- EDM-423: Adapt K8s lexer to support RHS equality symbol (asafbss)
- EDM-423: Modify k8s containment operator; remove support for 'in' and 'notin' on JSON fields (asafbss)
- EDM-423: Remove selector casting; implement a whitelist of selectors per resource for stricter filtering (asafbss)
- EDM-423: Move alias back to label in API while keeping it as a field in device model (asafbss)
- EDM-423: Add documentation for Field Selectors (asafbss)
- EDM-666: Updated selector names to match the documented API (asafbss)
- EDM-664: Added selector support for Spec field in field-selector (asafbss)
- EDM-423: Introduce Alias Field for Device Metadata (asafbss)
- EDM-423: Add more Kubernetes operators to enhance selectors for fields (asafbss)
- EDM-423: Kubernetes vanilla label selector (asafbss)
- NO-ISSUE: Fix linter and spellchecker issues (Frank A. Zdarsky)
- NO-ISSUE: Fix path-filter (Frank A. Zdarsky)
- EDM-694: Add Valkey key value store service (Ricardo Noriega)
- EDM-694: Use primary IP and nip.io domains (Ricardo Noriega)
- EDM-694: Add FlightCtl Network to slice (Ricardo Noriega)
- EDM-694: Add Makefile Quadlet targets for deploy and clean (Ricardo Noriega)
- EDM-694: Initial FlightCtl deployment with Quadlets (Ricardo Noriega)
- NO-ISSUE: Update config storage API (Avishay Traeger)
- EDM-578: Remove templateversion populate task (Avishay Traeger)
- EDM-582: Freeze HTTP configurations (Avishay Traeger)
- EDM-583: Freeze k8s secret configurations (Avishay Traeger)
- EDM-581: Freeze git configurations (Avishay Traeger)
- NO-ISSUE: Remove device's TV annotation if removed from fleet (Avishay Traeger)
- EDM-580: Deploy Valkey for frozen configurations (Avishay Traeger)
- EDM-578: Separate fleet validate and device render logic (Avishay Traeger)
- EDM-420: Enable to run devicesimulator on multiple hosts (#650) (Gregory Shilin)
- NO-ISSUE: Improve UpdateDeviceWithRetries to handle all errors (Miguel Angel Ajo)
- EDM-446: device/fileio: ensure readable paths in PathExists (Sam Batschelet)
- EDM-656: agent: clarify completed status (Sam Batschelet)
- EDM-656: agent: ensure Shutdown is threadsafe (Sam Batschelet)
- EDM-656: agent/device improve systemd status reporting (Sam Batschelet)
- NO-ISSUE: console: simplify exec test case (Sam Batschelet)
- EDM-673: Enable exposing APIs via Gateway (rawagner)
- EDM-650: Load k8s ca.crt when deployed on k8s (rawagner)

* Wed Dec 11 2024 Sam Batschelet <sbatsche@redhat.com> - 0.3.0-1.20241211143226238456.hooks.exec.115.gcae26e9d
- wip (Sam Batschelet)
- EDM-848: revert tzdata host mount (#690) (Sam Batschelet)
- EDM-876: Fix helm template error (Celia Amador)
- EDM-788: Add init script for PostgreSQL to set master user as superuser (#679) (Assaf Albo)
- NO-ISSUE: redeploy kind pods for development (Miguel Angel Ajo)
- NO-ISSUE: move ownership od status exporters to managers (Sam Batschelet)
- EDM-853: spec/queue: ensure item requeue backoff only if tried  (#696) (Sam Batschelet)
- EDM-642: Remove sorting mechanism for TP phase (#701) (Assaf Albo)
- MGMT-17672: Document device statuses (Frank A. Zdarsky)
- EDM-417: Get renderedVersion from DB (Frank A. Zdarsky)
- EDM-417: Update unit-tests for server-side status (Frank A. Zdarsky)
- EDM-417: Do not report retries, fail faster (Frank A. Zdarsky)
- EDM-417: Compute service-side status (Frank A. Zdarsky)
- EDM-649: Add specific error message when CSR approval fails (Lily Sturmann)
- EDM-649: Remove extraneous CN len check (Lily Sturmann)
- EDM-842: transient networking error should not mark agent degraded (Sam Batschelet)
- EDM-772: Move UI chart to main repo (#673) (Rastislav Wagner)
- NO-ISSUE: agent: ensure bootstrap status (Sam Batschelet)
- EDM-761: Document lifecycle hooks (Frank A. Zdarsky)
- EDM-764: Update Hooks API implementation (Frank A. Zdarsky)
- EDM-764: Update Hooks API (Frank A. Zdarsky)
- EDM-659: agent/fileio: ensure ignition gid, uid and mode (Sam Batschelet)
- NO-ISSUE: Pass internalOpenShiftApiUrl from values to api-config CM (#680) (Rastislav Wagner)
- EDM-424: add update policy api and validation (Sam Batschelet)
- EDM-420: Increase DB parameters (#669) (Gregory Shilin)
- NO-ISSUE: Append UI port to valid redirect URIs (rawagner)
- EDM-390: Add decommissioning route to device API (Lily Sturmann)
- EDM-417: Centralize API consts (Frank A. Zdarsky)
- NO-ISSUE: add command to prepare e2e test (sserafin)
- EDM-621: Fix microshift registration docs (Avishay Traeger)
- EDM-767: Actually update ignition mount path (Avishay Traeger)
- EDM-708: Reduce git access (Avishay Traeger)
- NO-ISSUE: Remove duplicated code in git helpers (Avishay Traeger)
- MGMT-17672: device: align update status phases (Sam Batschelet)
- Add developer documentation for field-selector (#658) (Assaf Albo)
- EDM-681: Allow field selector when 'summaryOnly' is enabled (#656) (Assaf Albo)
- EDM-704: Automate test 75991 (sserafin)
- EDM-695: typo fix (Gregory Shilin)
- EDM-695: typo fix (Gregory Shilin)
- EDM-695: typo fix (Gregory Shilin)
- EDM-695: typo fix (Gregory Shilin)
- EDM-695: created a document with instructions to run devicesimulator (Gregory Shilin)
- EDM-695: Logger verbosity flag was added (-v) (#651) (Gregory Shilin)
- EDM-423: Fix lexer state for nested exists/notexists selectors (#655) (Assaf Albo)
- EDM-423: Adapt K8s lexer to support RHS equality symbol (asafbss)
- EDM-423: Modify k8s containment operator; remove support for 'in' and 'notin' on JSON fields (asafbss)
- EDM-423: Remove selector casting; implement a whitelist of selectors per resource for stricter filtering (asafbss)
- EDM-423: Move alias back to label in API while keeping it as a field in device model (asafbss)
- EDM-423: Add documentation for Field Selectors (asafbss)
- EDM-666: Updated selector names to match the documented API (asafbss)
- EDM-664: Added selector support for Spec field in field-selector (asafbss)
- EDM-423: Introduce Alias Field for Device Metadata (asafbss)
- EDM-423: Add more Kubernetes operators to enhance selectors for fields (asafbss)
- EDM-423: Kubernetes vanilla label selector (asafbss)
- NO-ISSUE: Fix linter and spellchecker issues (Frank A. Zdarsky)
- NO-ISSUE: Fix path-filter (Frank A. Zdarsky)
- EDM-694: Add Valkey key value store service (Ricardo Noriega)
- EDM-694: Use primary IP and nip.io domains (Ricardo Noriega)
- EDM-694: Add FlightCtl Network to slice (Ricardo Noriega)
- EDM-694: Add Makefile Quadlet targets for deploy and clean (Ricardo Noriega)
- EDM-694: Initial FlightCtl deployment with Quadlets (Ricardo Noriega)
- NO-ISSUE: Update config storage API (Avishay Traeger)
- EDM-578: Remove templateversion populate task (Avishay Traeger)
- EDM-582: Freeze HTTP configurations (Avishay Traeger)
- EDM-583: Freeze k8s secret configurations (Avishay Traeger)
- EDM-581: Freeze git configurations (Avishay Traeger)
- NO-ISSUE: Remove device's TV annotation if removed from fleet (Avishay Traeger)
- EDM-580: Deploy Valkey for frozen configurations (Avishay Traeger)
- EDM-578: Separate fleet validate and device render logic (Avishay Traeger)
- EDM-420: Enable to run devicesimulator on multiple hosts (#650) (Gregory Shilin)
- NO-ISSUE: Improve UpdateDeviceWithRetries to handle all errors (Miguel Angel Ajo)
- EDM-446: device/fileio: ensure readable paths in PathExists (Sam Batschelet)
- EDM-656: agent: clarify completed status (Sam Batschelet)
- EDM-656: agent: ensure Shutdown is threadsafe (Sam Batschelet)
- EDM-656: agent/device improve systemd status reporting (Sam Batschelet)
- NO-ISSUE: console: simplify exec test case (Sam Batschelet)
- EDM-673: Enable exposing APIs via Gateway (rawagner)
- EDM-650: Load k8s ca.crt when deployed on k8s (rawagner)

* Mon Nov 4 2024 Miguel Angel Ajo <majopela@redhat.com> - 0.3.0-1
- Move the Release field to -1 so we avoid auto generating packages
  with -5 all the time.
* Wed Aug 21 2024 Sam Batschelet <sbatsche@redhat.com> - 0.0.1-5
- Add must-gather script to provide a simple mechanism to collect agent debug
* Wed Aug 7 2024 Sam Batschelet <sbatsche@redhat.com> - 0.0.1-4
- Add basic greenboot support for failed flightctl-agent service
* Wed Mar 13 2024 Ricardo Noriega <rnoriega@redhat.com> - 0.0.1-3
- New specfile for both CLI and agent packages
