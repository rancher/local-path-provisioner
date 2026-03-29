# syntax=docker/dockerfile:1.7.0

FROM registry.suse.com/bci/golang:1.26

ARG TARGETPLATFORM
RUN if [ "$TARGETPLATFORM" != "linux/amd64" ] && [ "$TARGETPLATFORM" != "linux/arm64" ] && [ "$TARGETPLATFORM" != "linux/arm/v7" ] && [ "$TARGETPLATFORM" != "linux/ppc64le" ] && [ "$TARGETPLATFORM" != "linux/riscv64" ] && [ "$TARGETPLATFORM" != "linux/s390x" ]; then \
    echo "Error: Unsupported TARGETPLATFORM: $TARGETPLATFORM" && \
    exit 1; \
    fi

ENV ARCH ${TARGETPLATFORM#linux/}
ENV ARCH ${ARCH%/v7}

RUN zypper -n ref && \
    zypper update -y

RUN zypper -n install busybox zlib

COPY bin/local-path-provisioner-${ARCH} /usr/bin/local-path-provisioner
CMD ["local-path-provisioner"]
