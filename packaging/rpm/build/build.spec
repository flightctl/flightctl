# if this is a buggy version of go we need to set GOPROXY as workaround
# see https://github.com/golang/go/issues/61928
GOENVFILE=$(go env GOROOT)/go.env
if [[ ! -f "${GOENVFILE}" ]]; then
    export GOPROXY='https://proxy.golang.org,direct'
fi

SOURCE_GIT_TAG=$(echo %{version} | tr '~' '-') \
SOURCE_GIT_TREE_STATE=clean \
SOURCE_GIT_COMMIT=$(echo %{version} | awk -F'[-~]g' '{print $2}') \
SOURCE_GIT_TAG_NO_V=%{version} \
%if 0%{?rhel} == 9
    %make_build build-cli build-agent
%else
    DISABLE_FIPS="true" %make_build build-cli build-agent
%endif
