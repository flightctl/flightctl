# Licences install commands
%global licences_install_commands \
rm -f licenses.list; \
find . -type f -name LICENSE -or -name License | while read LICENSE_FILE; do \
    echo "%{_datadir}/licenses/%{NAME}/${LICENSE_FILE}" >> licenses.list; \
done; \
mkdir -vp "%{buildroot}%{_datadir}/licenses/%{NAME}"; \
cp LICENSE "%{buildroot}%{_datadir}/licenses/%{NAME}"