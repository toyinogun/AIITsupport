# Noord's on prem directory: Samba AD that provisions itself on first boot.
# The entrypoint lays down the security skeleton (top OUs, svc-itsupport,
# delegation ACEs, lockout policy) before samba starts serving. Content comes
# later from cmd/seed over LDAPS. See docs/specs/0004-noord-directory.

# debian:trixie-slim, pinned by digest so both arches build the same base.
FROM debian:trixie-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        samba samba-ad-dc samba-ad-provision ldb-tools ldap-utils ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && rm -f /etc/samba/smb.conf \
    && rm -rf /var/lib/samba/* /var/cache/samba/*

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh

EXPOSE 636
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
