podman run --rm -it \
    --name otelcol \
    --net=host \
    -v "$(pwd)/otel-client-with-cn-auth.yaml":/etc/otel2/config.yaml:Z \
    -v "$(pwd)//otel-certs":/etc/otel3:Z \
    otel/opentelemetry-collector-contrib:0.128.0 \
    --config /etc/otel2/config.yaml