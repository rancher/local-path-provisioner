# syntax=docker/dockerfile:1.7.0

FROM golang:1.26.2-alpine

ARG TARGETPLATFORM
ARG TARGETARCH

RUN if [ "$TARGETPLATFORM" != "linux/amd64" ] && [ "$TARGETPLATFORM" != "linux/arm64" ] && [ "$TARGETPLATFORM" != "linux/arm/v7" ] && [ "$TARGETPLATFORM" != "linux/ppc64le" ] && [ "$TARGETPLATFORM" != "linux/riscv64" ] && [ "$TARGETPLATFORM" != "linux/s390x" ]; then \
    echo "Error: Unsupported TARGETPLATFORM: $TARGETPLATFORM" && \
    exit 1; \
    fi

RUN apk update
RUN apk upgrade --no-cache busybox zlib

COPY bin/local-path-provisioner-${TARGETARCH} /usr/bin/local-path-provisioner
CMD ["local-path-provisioner"]
