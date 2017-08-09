FROM alpine:3.6
MAINTAINER mkargaki@redhat.com

RUN apk update --no-cache && \
	apk add ca-certificates && \
	update-ca-certificates

COPY flake-detector /flake-detector

ENTRYPOINT ["/flake-detector"]
