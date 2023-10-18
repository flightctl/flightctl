#FROM registry.access.redhat.com/ubi9/go-toolset as build
FROM golang:1.21 as build
WORKDIR /app
COPY ./ .

RUN make build

FROM registry.access.redhat.com/ubi9/ubi-micro
WORKDIR /app
COPY --from=build /app/bin/flightctl-server .
CMD ./flightctl-server