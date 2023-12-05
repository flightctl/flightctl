FROM registry.access.redhat.com/ubi9/go-toolset:1.20 as build
WORKDIR /app
COPY ./ .
USER 0
RUN make build

FROM registry.access.redhat.com/ubi9/ubi-micro
WORKDIR /app
COPY --from=build /app/bin/flightctl-server .
CMD ./flightctl-server