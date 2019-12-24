FROM golang:1.12.9-alpine

RUN apk add --no-cache ca-certificates git

ENV \
  GO111MODULE=on \
  CGO_ENABLED=0 \
  GOOS=linux

WORKDIR /go/src/github.com/rancher/local-path-provisioner

ADD . .
RUN go build -v -installsuffix cgo -ldflags="-w -s" -o /go/bin/local-path-provisioner .


FROM alpine:3.10
COPY --from=0 /go/bin /go/bin
ENV PATH='/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
CMD /go/bin/local-path-provisioner
