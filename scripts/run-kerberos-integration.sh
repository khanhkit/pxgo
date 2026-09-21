#!/usr/bin/env bash
set -euo pipefail

for cmd in go kdb5_util kadmin.local krb5kdc kinit kdestroy; do
  command -v "$cmd" >/dev/null 2>&1 || {
    echo "kerberos-integration: required command missing: $cmd" >&2
    exit 1
  }
done

realm=PXGO.TEST
principal=pxgo-ci@PXGO.TEST
password=pxgo-ci-password
root=$(mktemp -d)
kdc_pid=

cleanup() {
  if [[ -n "${kdc_pid}" ]]; then
    kill "${kdc_pid}" 2>/dev/null || true
    wait "${kdc_pid}" 2>/dev/null || true
  fi
  rm -rf "$root"
}
trap cleanup EXIT

cat >"$root/krb5.conf" <<EOF
[libdefaults]
  default_realm = $realm
  dns_lookup_kdc = false
  dns_lookup_realm = false
  rdns = false
  ticket_lifetime = 1h
  renew_lifetime = 7d

[realms]
  $realm = {
    kdc = 127.0.0.1:61088
  }
EOF

cat >"$root/kdc.conf" <<EOF
[kdcdefaults]
  kdc_ports = 61088
  kdc_tcp_ports = 61088

[realms]
  $realm = {
    database_name = $root/principal
    key_stash_file = $root/.k5.$realm
    acl_file = $root/kadm5.acl
    max_life = 10h
    max_renewable_life = 7d
  }
EOF

printf '*/admin@%s *\n' "$realm" >"$root/kadm5.acl"

export KRB5_CONFIG="$root/krb5.conf"
export KRB5_KDC_PROFILE="$root/kdc.conf"
export KRB5CCNAME="FILE:$root/ready.ccache"

kdb5_util create -s -P pxgo-master-password -r "$realm" >/dev/null
kadmin.local -r "$realm" -q "addprinc -pw $password $principal" >/dev/null

krb5kdc -n -r "$realm" >"$root/kdc.log" 2>&1 &
kdc_pid=$!

ready=false
for _ in $(seq 1 50); do
  if printf '%s\n' "$password" | kinit "$principal" >/dev/null 2>&1; then
    kdestroy >/dev/null 2>&1 || true
    ready=true
    break
  fi
  sleep 0.1
done

if [[ "$ready" != true ]]; then
  cat "$root/kdc.log" >&2
  echo "kerberos-integration: KDC did not become ready" >&2
  exit 1
fi

export PXGO_KERBEROS_PRINCIPAL="$principal"
export PXGO_KERBEROS_PASSWORD="$password"
export PXGO_KERBEROS_FLAVOR=mit
unset KRB5CCNAME

go test -tags=kerberos_integration ./internal/kerberos -run '^TestKerberosIntegration' -count=1 -v
