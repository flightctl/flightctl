%build
    # if this is a buggy version of go we need to set GOPROXY as workaround
    # see https://github.com/golang/go/issues/61928
    GOENVFILE=$(go env GOROOT)/go.env
    if [[ ! -f "${GOENVFILE}" ]]; then
        export GOPROXY='https://proxy.golang.org,direct'
    fi

    # Prefer values injected by Makefile/CI; fall back to RPM macros when unset
    SOURCE_GIT_TAG="%{?SOURCE_GIT_TAG:%{SOURCE_GIT_TAG}}%{!?SOURCE_GIT_TAG:%(echo "v%{version}" | tr '~' '-')}" \
    SOURCE_GIT_TREE_STATE="%{?SOURCE_GIT_TREE_STATE:%{SOURCE_GIT_TREE_STATE}}%{!?SOURCE_GIT_TREE_STATE:clean}" \
    SOURCE_GIT_COMMIT="%{?SOURCE_GIT_COMMIT:%{SOURCE_GIT_COMMIT}}%{!?SOURCE_GIT_COMMIT:%(echo %{version} | grep -o '[-~]g[0-9a-f]*' | sed 's/[-~]g//' || echo unknown)}" \
    SOURCE_GIT_TAG_NO_V="%{?SOURCE_GIT_TAG_NO_V:%{SOURCE_GIT_TAG_NO_V}}%{!?SOURCE_GIT_TAG_NO_V:%{version}}" \
    %if 0%{?rhel} == 9
        %make_build build-cli build-agent build-restore
    %else
        DISABLE_FIPS="true" %make_build build-cli build-agent build-restore
    %endif
