FROM ubuntu:16.04

RUN apt-get update && apt-get install -y curl vim nfs-common iproute dnsutils iputils-ping telnet

COPY bin /usr/local/sbin/

VOLUME /usr/local/sbin
CMD ["local-path-provisioner"]
