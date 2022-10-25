FROM alpine

RUN apk update
RUN apk upgrade --no-cache busybox zlib

COPY bin/local-path-provisioner /usr/bin/
CMD ["local-path-provisioner"]
