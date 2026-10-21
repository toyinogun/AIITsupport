#!/bin/sh
# First boot: provision the domain and lay down the security skeleton, offline
# against sam.ldb, before samba serves anything. A plain restart skips straight
# to starting samba: in Compose the container's own filesystem is the state; in
# a cluster a volume holds /var/lib/samba and /etc/samba (spec 0015). The certs
# are copied in on every boot, so a renewed certificate takes effect on restart.
set -eu

PRIVATE=/var/lib/samba/private
SAM="$PRIVATE/sam.ldb"
TLS_DIR="$PRIVATE/tls-noord"
# Written only after provisioning, the skeleton, and the delegation all
# succeed. A domain without it was cut off halfway and is provisioned again.
MARKER=/var/lib/samba/.noord-provisioned

SAMBA_REALM="${SAMBA_REALM:-AD.NOORD.EXAMPLE}"
SAMBA_DOMAIN="${SAMBA_DOMAIN:-NOORD}"
SAMBA_LOCKOUT_THRESHOLD="${SAMBA_LOCKOUT_THRESHOLD:-5}"
SAMBA_LOCKOUT_DURATION="${SAMBA_LOCKOUT_DURATION:-15}"
SAMBA_LOCKOUT_WINDOW="${SAMBA_LOCKOUT_WINDOW:-15}"

# Base schema GUIDs, the same in every forest. The permission wall test proves
# them: a wrong one leaves svc-itsupport unable to unlock or edit groups.
GUID_LOCKOUT_TIME=28630ebf-41d5-11d1-a9c1-0000f80367c1
GUID_MEMBER=bf9679c0-0de6-11d0-a285-00aa003049e2
GUID_CLASS_USER=bf967aba-0de6-11d0-a285-00aa003049e2
GUID_CLASS_GROUP=bf967a9c-0de6-11d0-a285-00aa003049e2
# The Reset Password extended right (User-Force-Change-Password).
GUID_RESET_PASSWORD=00299570-246d-11d0-a768-00aa006e0529

fail() {
	echo "entrypoint: $*" >&2
	exit 1
}

require_env() {
	for name in "$@"; do
		eval "value=\${$name:-}"
		[ -n "$value" ] || fail "$name is not set"
	done
}

# Samba refuses a TLS key that is not owned by its user with mode 0600, and a
# bind mounted file carries the host's owner, so copy the certs in.
install_certs() {
	for f in ca.pem server.pem server.key; do
		[ -r "/certs/$f" ] || fail "/certs/$f is missing, run make certs"
	done
	mkdir -p "$TLS_DIR"
	cp /certs/ca.pem /certs/server.pem /certs/server.key "$TLS_DIR/"
	chmod 0600 "$TLS_DIR/server.key"
	chmod 0644 "$TLS_DIR/ca.pem" "$TLS_DIR/server.pem"
}

provision() {
	require_env SAMBA_ADMIN_PASSWORD SAMBA_SVC_PASSWORD SAMBA_ZAMMAD_PASSWORD
	rm -f /etc/samba/smb.conf
	# stdout is progress chatter; errors go to stderr and still show.
	samba-tool domain provision \
		--realm="$SAMBA_REALM" \
		--domain="$SAMBA_DOMAIN" \
		--server-role=dc \
		--dns-backend=NONE \
		--host-name=samba \
		--adminpass="$SAMBA_ADMIN_PASSWORD" \
		--option="acl_xattr:security_acl_name = user.NTACL" \
		--option="tls enabled = yes" \
		--option="tls keyfile = $TLS_DIR/server.key" \
		--option="tls certfile = $TLS_DIR/server.pem" \
		--option="tls cafile = $TLS_DIR/ca.pem" \
		--option="server services = -dns" \
		--option="dns update command = /bin/true" \
		--option="ldap server require strong auth = yes" >/dev/null
	# Samba keeps accepting a replaced password for 60 minutes by default
	# (old password allowed period), LDAP binds included, so a reset would not
	# shut out a leaked password (spec 0011, journal 0038); 0 ends it at once.
	# Provision silently drops this --option, so write it into [global].
	sed -i 's/^\[global\]$/[global]\n\told password allowed period = 0/' /etc/samba/smb.conf
	grep -q "old password allowed period = 0" /etc/samba/smb.conf || fail "could not set old password allowed period"
}

base_dn() {
	ldbsearch -H "$SAM" -s base -b "" defaultNamingContext |
		sed -n 's/^defaultNamingContext: //p'
}

skeleton() {
	base="$1"
	samba-tool domain passwordsettings set -H "$SAM" \
		--account-lockout-threshold="$SAMBA_LOCKOUT_THRESHOLD" \
		--account-lockout-duration="$SAMBA_LOCKOUT_DURATION" \
		--reset-account-lockout-after="$SAMBA_LOCKOUT_WINDOW" >/dev/null

	ldbmodify -H "$SAM" <<-EOF
	dn: $base
	changetype: modify
	replace: ms-DS-MachineAccountQuota
	ms-DS-MachineAccountQuota: 0
	EOF

	for ou in "OU=Noord" "OU=Users,OU=Noord" "OU=Groups,OU=Noord" \
		"OU=Service Accounts,OU=Noord" "OU=Admin Accounts,OU=Noord"; do
		samba-tool ou add "$ou" -H "$SAM"
	done

	samba-tool user add svc-itsupport "$SAMBA_SVC_PASSWORD" \
		--userou="OU=Service Accounts,OU=Noord" -H "$SAM"
	samba-tool user setexpiry svc-itsupport --noexpiry -H "$SAM"

	# Zammad's LDAP bind (spec 0006). No ACEs of its own: it reads users as
	# any authenticated account can, and every write fails.
	samba-tool user add svc-zammad "$SAMBA_ZAMMAD_PASSWORD" \
		--userou="OU=Service Accounts,OU=Noord" -H "$SAM"
	samba-tool user setexpiry svc-zammad --noexpiry -H "$SAM"

	# The reset page's account (spec 0011). Optional, like the feature: no
	# password means no account and no rights.
	if [ -n "${SAMBA_PWRESET_PASSWORD:-}" ]; then
		samba-tool user add svc-pwreset "$SAMBA_PWRESET_PASSWORD" \
			--userou="OU=Service Accounts,OU=Noord" -H "$SAM"
		samba-tool user setexpiry svc-pwreset --noexpiry -H "$SAM"
	fi
}

# svc-itsupport may clear lockoutTime on users under OU=Users and write member
# on groups under OU=Groups. CIIO: inherited by child objects only, never the
# OU itself. Nothing is delegated anywhere else.
delegate() {
	base="$1"
	sid="$(ldbsearch -H "$SAM" -b "$base" "(sAMAccountName=svc-itsupport)" objectSid |
		sed -n 's/^objectSid: //p')"
	[ -n "$sid" ] || fail "svc-itsupport has no objectSid"

	samba-tool dsacl set -H "$SAM" \
		--objectdn="OU=Users,OU=Noord,$base" \
		--sddl="(OA;CIIO;WP;$GUID_LOCKOUT_TIME;$GUID_CLASS_USER;$sid)" >/dev/null
	samba-tool dsacl set -H "$SAM" \
		--objectdn="OU=Groups,OU=Noord,$base" \
		--sddl="(OA;CIIO;WP;$GUID_MEMBER;$GUID_CLASS_GROUP;$sid)" >/dev/null

	# svc-pwreset may reset passwords and clear lockoutTime on users under
	# OU=Users, nothing else (spec 0011, AC-15).
	if [ -n "${SAMBA_PWRESET_PASSWORD:-}" ]; then
		reset_sid="$(ldbsearch -H "$SAM" -b "$base" "(sAMAccountName=svc-pwreset)" objectSid |
			sed -n 's/^objectSid: //p')"
		[ -n "$reset_sid" ] || fail "svc-pwreset has no objectSid"
		samba-tool dsacl set -H "$SAM" \
			--objectdn="OU=Users,OU=Noord,$base" \
			--sddl="(OA;CIIO;CR;$GUID_RESET_PASSWORD;$GUID_CLASS_USER;$reset_sid)" >/dev/null
		samba-tool dsacl set -H "$SAM" \
			--objectdn="OU=Users,OU=Noord,$base" \
			--sddl="(OA;CIIO;WP;$GUID_LOCKOUT_TIME;$GUID_CLASS_USER;$reset_sid)" >/dev/null
	fi
}

# Samba starts but never listens unless the hostname is samba, even when
# provisioned under another name (journal 0008). Fail loudly instead of
# hanging. Compose: hostname: samba; the cluster: a Deployment with hostname.
[ "$(hostname)" = samba ] || fail "the container hostname is $(hostname), it must be samba"

if [ ! -f "$MARKER" ]; then
	if [ -f "$SAM" ]; then
		echo "entrypoint: found a domain without its marker, provisioning again" >&2
	fi
	# Empty the directories, not remove them: in a cluster they are mounts.
	find /var/lib/samba /etc/samba -mindepth 1 -delete
	install_certs
	provision
	base="$(base_dn)"
	[ -n "$base" ] || fail "could not read the base DN after provisioning"
	skeleton "$base"
	delegate "$base"
	touch "$MARKER"
	echo "entrypoint: provisioned $SAMBA_REALM, skeleton and delegation in place"
else
	install_certs
fi

exec samba --foreground --no-process-group --debug-stdout
