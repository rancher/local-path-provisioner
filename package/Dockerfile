# syntax=docker/dockerfile:1.7.0

FROM alpine

ARG TARGETPLATFORM
RUN if [ "$TARGETPLATFORM" != "linux/amd64" ] && [ "$TARGETPLATFORM" != "linux/arm64" ]; then \
    echo "Error: Unsupported TARGETPLATFORM: $TARGETPLATFORM" && \
    exit 1; \
    fi

ENV ARCH ${TARGETPLATFORM#linux/}

RUN apk update
RUN apk upgrade --no-cache busybox zlib

COPY bin/local-path-provisioner-${ARCH} /usr/bin/local-path-provisioner
CMD ["local-path-provisioner"]
