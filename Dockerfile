# Start with busybox, but with libc.so.6
FROM busybox:ubuntu-14.04

MAINTAINER Michael Stapelberg <michael@robustirc.net>

# So that we can run as unprivileged user inside the container.
RUN echo 'nobody:x:99:99:nobody:/:/bin/sh' >> /etc/passwd

USER nobody

ADD ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ADD robustirc /usr/bin/robustirc

# RobustIRC listens on port 443 by default, but a port in the dynamic port
# range (49152 to 65535) should be used when exposing this port on the host.
EXPOSE 8443

VOLUME ["/var/lib/robustirc"]

# sqlite3 uses TMPDIR to store temporary files in, but when this container is
# run read-only without /tmp as a volume, that would fail. Hence, use
# /var/lib/robustirc, which must be writeable.
ENV TMPDIR=/var/lib/robustirc

# The following flags have to be specified when starting this container:
# -peer_addr
# -network_name
# -network_password
# -tls_cert_path
# -tls_key_path
# Refer to -help for documentation on them.
ENTRYPOINT ["/usr/bin/robustirc", "-listen=:8443"]
