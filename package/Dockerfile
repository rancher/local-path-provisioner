# syntax=docker/dockerfile:1.26.0

# Helper stage: validate the target platform and source a CA certificate bundle.
# The final image is scratch, so it carries only the statically-linked binary and
# these certificates - no shell, package manager or Go toolchain.
FROM alpine AS prep

ARG TARGETPLATFORM

RUN if [ "$TARGETPLATFORM" != "linux/amd64" ] && [ "$TARGETPLATFORM" != "linux/arm64" ] && [ "$TARGETPLATFORM" != "linux/arm/v7" ] && [ "$TARGETPLATFORM" != "linux/ppc64le" ] && [ "$TARGETPLATFORM" != "linux/riscv64" ] && [ "$TARGETPLATFORM" != "linux/s390x" ]; then \
    echo "Error: Unsupported TARGETPLATFORM: $TARGETPLATFORM" && \
    exit 1; \
    fi

RUN apk add --no-cache ca-certificates

FROM scratch

ARG TARGETARCH

COPY --from=prep /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY bin/local-path-provisioner-${TARGETARCH} /usr/bin/local-path-provisioner

CMD ["/usr/bin/local-path-provisioner"]
