# Building with “go build” will work just fine.
# This file just exists to build Docker containers.

VERSION := $(shell git describe --tags --always) ($(shell git log --pretty=format:%cd --date=short -n1))

.PHONY: container

all: container

container:
	go generate
	CGO_ENABLED=0 GOARCH=arm64 go build -ldflags "-X 'main.Version=${VERSION}'"
	mv robustirc robustirc.arm64
	CGO_ENABLED=0 go build -ldflags "-X 'main.Version=${VERSION}'"
	cp robustirc robustirc.amd64
	# This list is from go/src/crypto/x509/root_unix.go.
	# TODO: once https://github.com/golang/go/issues/43958 is fixed,
	# switch to golang.org/x/crypto/x509roots/fallback
	install $(shell ls \
/etc/ssl/certs/ca-certificates.crt \
/etc/pki/tls/certs/ca-bundle.crt \
/etc/ssl/ca-bundle.pem \
/etc/ssl/cert.pem \
/usr/local/share/certs/ca-root-nss.crt \
/etc/pki/tls/cacert.pem \
/etc/certs/ca-certificates.crt \
2>&- | head -1) ca-certificates.crt
	DOCKER_BUILDKIT=1 docker buildx create --use
	DOCKER_BUILDKIT=1 docker buildx build --rm --push -t=robustirc/robustirc --platform linux/arm64/v8,linux/amd64 .

container-refuse:
	(cd cmd/robustirc-refuse && go build && docker build --rm -t=robustirc/refuse .)
