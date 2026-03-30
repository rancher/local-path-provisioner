# syntax=docker/dockerfile:1.7.0

# Build stage: cross-compile binaries for all architectures
FROM --platform=$BUILDPLATFORM registry.suse.com/bci/golang:1.26 AS builder
RUN zypper -n ref && zypper update -y
RUN zypper -n install bash git gcc
WORKDIR /src
COPY . .
RUN ./scripts/build

# Test stage: run unit tests
FROM builder AS test
RUN ./scripts/test

# Validate stage: lint and format check
FROM test AS validate
RUN curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin latest
RUN ./scripts/validate

# Binary output stage: extract binaries only (build only, no test/validate)
FROM scratch AS binary
COPY --from=builder /src/bin/ /

# CI binary output stage: extract binaries after build + test + validate
FROM scratch AS ci-binary
COPY --from=validate /src/bin/ /

# Final runtime image
FROM registry.suse.com/bci/golang:1.26

ARG TARGETPLATFORM
ARG TARGETARCH

RUN if [ "$TARGETPLATFORM" != "linux/amd64" ] && [ "$TARGETPLATFORM" != "linux/arm64" ] && [ "$TARGETPLATFORM" != "linux/arm/v7" ] && [ "$TARGETPLATFORM" != "linux/ppc64le" ] && [ "$TARGETPLATFORM" != "linux/riscv64" ] && [ "$TARGETPLATFORM" != "linux/s390x" ]; then \
    echo "Error: Unsupported TARGETPLATFORM: $TARGETPLATFORM" && \
    exit 1; \
    fi

RUN zypper -n ref && \
    zypper update -y

RUN zypper -n install busybox zlib

COPY --from=builder /src/bin/local-path-provisioner-${TARGETARCH} /usr/bin/local-path-provisioner
CMD ["local-path-provisioner"]
