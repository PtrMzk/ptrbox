#!/bin/bash
# =============================================================================
# 20-firewall.sh - default-deny egress. Runs as root inside the guest.
#
# The ruleset is WRITTEN here and ACTIVATED on the next boot: the user
# provisioning scripts still need open network for their installers, so the
# unit is enabled but not started. `ptrbox new` reboots the VM afterwards,
# which is the moment the wall goes up.
#
# Widening any rule here is a security decision, not a bugfix.
# =============================================================================
set -eux

if [ -f /var/lib/ptrbox/firewall.done ]; then
  exit 0
fi

# Root-owned + chmod 600 so the agent user can't read or edit it - which only
# means something combined with the sudo removal in 90-harden.sh.
cat >/etc/nftables-sandbox.nft <<'NFT'
table inet sandbox {
  chain output {
    # Hook every OUTGOING packet; the verdict for anything not matched by a
    # rule below is DROP. Default-deny: we enumerate what is allowed instead
    # of trying to enumerate everything bad.
    type filter hook output priority 0; policy drop;
    oif "lo" accept                     # loopback: the VM talking to itself
                                        # (dev servers, tests, local DBs)
    ct state established,related accept # connection tracking: once a permitted
                                        # connection is open, its packets keep
                                        # flowing both ways
    # DNS: pinned to the resolvers written to /etc/resolv.conf below. Unpinned
    # port-53 egress is a covert exfiltration channel - any process could
    # smuggle data in queries to an attacker-run nameserver. Pinning narrows
    # (does not close: DNS tunneling can transit recursive resolvers) that
    # channel. Note these are v4 rules; IPv6 egress has no allow rules at all
    # in this table, so it is implicitly default-denied.
    ip daddr { __DNS_NFT_SET__ } udp dport 53 accept
    ip daddr { __DNS_NFT_SET__ } tcp dport 53 accept
    # THE one road out: the Squid allowlist proxy on the Mac, reached at the
    # vzNAT gateway address.
    ip daddr __PROXY_HOST__ tcp dport __PROXY_PORT__ accept
  }
}
NFT
chmod 600 /etc/nftables-sandbox.nft

# systemd unit that loads the ruleset at every boot.
# Type=oneshot: run the command once and exit (it's not a daemon).
# RemainAfterExit: systemd still reports the unit "active" afterwards, so
# `systemctl status sandbox-firewall` tells you the wall is up.
cat >/etc/systemd/system/sandbox-firewall.service <<'UNIT'
[Unit]
Description=Default-deny egress firewall (ptrbox sandbox)
After=network-online.target
Wants=network-online.target
[Service]
Type=oneshot
ExecStart=/usr/sbin/nft -f /etc/nftables-sandbox.nft
RemainAfterExit=yes
[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
# "enable" = start on future boots. Deliberately NOT "systemctl start": the
# user provisioning scripts still need open network for their installers.
systemctl enable sandbox-firewall.service

# Pin the resolver to match the firewall's DNS rules. A static resolv.conf made
# immutable (chattr +i) is the distro-agnostic way to stop any network manager
# (systemd-resolved, dhclient, networkd) from rewriting it back to the
# DHCP-provided resolver - which the firewall would then block, breaking all
# name resolution. This also bypasses Lima's built-in DNS relay entirely.
rm -f /etc/resolv.conf
# shellcheck disable=SC2043  # renders to a space-separated list of resolvers
for ns in __DNS_LIST__; do
  printf 'nameserver %s\n' "$ns" >>/etc/resolv.conf
done
chattr +i /etc/resolv.conf

mkdir -p /var/lib/ptrbox
touch /var/lib/ptrbox/firewall.done
