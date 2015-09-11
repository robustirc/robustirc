# Start with busybox, but with libc.so.6
FROM busybox:ubuntu-14.04

MAINTAINER Michael Stapelberg <michael@robustirc.net>

# So that we can run as unprivileged user inside the container.
RUN echo 'nobody:x:99:99:nobody:/:/bin/sh' >> /etc/passwd

USER nobody

ADD robustirc-refuse /usr/bin/robustirc-refuse

EXPOSE 6667

ENTRYPOINT ["/usr/bin/robustirc-refuse"]
