# Building with “go build” will work just fine.
# This file just exists to build Docker containers.

VERSION := '$(shell git describe --tags --always) ($(shell git log --pretty=format:%cd --date=short -n1))'

.PHONY: container

all: container

container:
	go build -ldflags "-X main.Version ${VERSION}"
	# This list is from go/src/crypto/x509/root_unix.go.
	install $(shell ls \
/etc/ssl/certs/ca-certificates.crt \
/etc/pki/tls/certs/ca-bundle.crt \
/etc/ssl/ca-bundle.pem \
/etc/ssl/cert.pem \
/usr/local/share/certs/ca-root-nss.crt \
/etc/pki/tls/cacert.pem \
/etc/certs/ca-certificates.crt \
2>&- | head -1) ca-certificates.crt
	docker build --rm -t=robustirc/robustirc .
