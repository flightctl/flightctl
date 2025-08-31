mkdir -p %{buildroot}/usr/bin
mkdir -p %{buildroot}/etc/flightctl
cp bin/flightctl %{buildroot}/usr/bin
mkdir -p %{buildroot}/usr/lib/systemd/system
mkdir -p %{buildroot}/%{_sharedstatedir}/flightctl
mkdir -p %{buildroot}/usr/lib/flightctl/custom-info.d
mkdir -p %{buildroot}/usr/lib/flightctl/hooks.d/{afterupdating,beforeupdating,afterrebooting,beforerebooting}
mkdir -p %{buildroot}/usr/lib/greenboot/check/required.d
install -m 0755 packaging/greenboot/flightctl-agent-running-check.sh %{buildroot}/usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
cp bin/flightctl-agent %{buildroot}/usr/bin
cp packaging/must-gather/flightctl-must-gather %{buildroot}/usr/bin
cp packaging/hooks.d/afterupdating/00-default.yaml %{buildroot}/usr/lib/flightctl/hooks.d/afterupdating
cp packaging/systemd/flightctl-agent.service %{buildroot}/usr/lib/systemd/system
bin/flightctl completion bash > flightctl-completion.bash
install -Dpm 0644 flightctl-completion.bash -t %{buildroot}/%{_datadir}/bash-completion/completions
bin/flightctl completion fish > flightctl-completion.fish
install -Dpm 0644 flightctl-completion.fish -t %{buildroot}/%{_datadir}/fish/vendor_completions.d/
bin/flightctl completion zsh > _flightctl-completion
install -Dpm 0644 _flightctl-completion -t %{buildroot}/%{_datadir}/zsh/site-functions/
