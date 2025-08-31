rm -f licenses.list

find . -type f -name LICENSE -or -name License | while read LICENSE_FILE; do
    echo "%{_datadir}/licenses/%{NAME}/${LICENSE_FILE}" >> licenses.list
done
mkdir -vp "%{buildroot}%{_datadir}/licenses/%{NAME}"
cp LICENSE "%{buildroot}%{_datadir}/licenses/%{NAME}"

mkdir -vp "%{buildroot}%{_docdir}/%{NAME}"

for DOC in docs examples .markdownlint-cli2.yaml README.md; do
    cp -vr "${DOC}" "%{buildroot}%{_docdir}/%{NAME}/${DOC}"
done
