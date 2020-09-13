FROM golang:1.15.2-alpine as builder

COPY . /program
WORKDIR /program
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o local-path-provisioner .

FROM alpine
COPY --from=builder /program/local-path-provisioner /usr/bin/
CMD ["/usr/bin/local-path-provisioner"]
