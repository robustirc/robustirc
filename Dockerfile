# Start with busybox, but with libc.so.6
FROM busybox:ubuntu-14.04

MAINTAINER Michael Stapelberg <michael@robustirc.net>

ADD robustirc /usr/bin/robustirc

# RobustIRC listens on port 443 by default, but a port in the dynamic port
# range (49152 to 65535) should be used when exposing this port on the host.
EXPOSE 443

VOLUME ["/var/lib/robustirc"]

# The following flags have to be specified when starting this container:
# -peer_addr
# -network_name
# -network_password
# -tls_cert_path
# -tls_key_path
# Refer to -help for documentation on them.
ENTRYPOINT ["/usr/bin/robustirc"]
