#!/usr/bin/env bash
# Administrator-only provisioning; NEVER invoked automatically by an MCP tool.
# Creates an isolated veth, not qdiscs on the host uplink (which carries the tunnel).
set -euo pipefail
ns=${1:-devspace-bash}
iface=${2:-ds-bash0}
rate=${3:-1048576} # bytes/sec per direction, shared by jobs in this namespace
[[ $EUID -eq 0 ]] || { echo 'Run this setup script as root, NOT the MCP server.' >&2; exit 1; }
[[ $ns =~ ^[a-zA-Z0-9_-]+$ && $iface =~ ^[a-zA-Z0-9_-]{1,15}$ && $rate =~ ^[0-9]+$ ]] || exit 1
(( rate >= 1024 && rate <= 100000000000 )) || { echo 'rate must be 1024..100000000000 bytes/sec'; exit 1; }
for cmd in ip tc nft sysctl; do command -v "$cmd" >/dev/null || { echo "Missing $cmd"; exit 1; }; done
[[ $(sysctl -n net.ipv4.ip_forward) == 1 ]] || { echo 'Administrator must first enable IPv4 forwarding: sysctl -w net.ipv4.ip_forward=1'; exit 1; }
[[ ! -e /run/netns/$ns && ! -e /etc/netns/$ns ]] || { echo 'Namespace or resolver directory already exists; refusing to overwrite.'; exit 1; }
! ip link show "$iface" >/dev/null 2>&1 || { echo 'Interface already exists; refusing to overwrite.'; exit 1; }
! nft list table ip devspace_bash_nat >/dev/null 2>&1 || { echo 'NAT table already exists; refusing to overwrite.'; exit 1; }
# This helper uses a dedicated /30. Review routes/firewall policy before running it.
! ip -4 route show | grep -q '10\.203\.0\.' || { echo '10.203.0.0/30 overlaps an existing route.'; exit 1; }
created_ns=0; created_nat=0
cleanup() {
  status=$?
  if (( status != 0 )); then
    (( created_ns == 0 )) || ip netns del "$ns" || true
    (( created_nat == 0 )) || nft delete table ip devspace_bash_nat || true
    ip link del "$iface" 2>/dev/null || true
    rm -f -- "/etc/netns/$ns/resolv.conf"
    rmdir -- "/etc/netns/$ns" 2>/dev/null || true
  fi
}
trap cleanup EXIT
ip netns add "$ns"; created_ns=1
ip link add "$iface" type veth peer name ds-peer0 netns "$ns"
ip address add 10.203.0.1/30 dev "$iface"
ip link set "$iface" up
ip -n "$ns" link set ds-peer0 name eth0
ip -n "$ns" address add 10.203.0.2/30 dev eth0
ip -n "$ns" link set lo up
ip -n "$ns" link set eth0 up
ip -n "$ns" route add default via 10.203.0.1
# Egress on the HOST veth is download TO the job.
# Ingress policing on the HOST veth caps upload FROM the job, never the tunnel.
bits=$((rate*8)); burst=$((rate/10)); (( burst >= 65536 )) || burst=65536
tc qdisc add dev "$iface" root tbf rate "${bits}bit" burst "$burst" latency 100ms
tc qdisc add dev "$iface" handle ffff: ingress
tc filter add dev "$iface" parent ffff: protocol all prio 1 matchall action police rate "${bits}bit" burst "$burst" conform-exceed drop
mkdir -p -- "/etc/netns/$ns"
printf 'nameserver 1.1.1.1\nnameserver 8.8.8.8\n' > "/etc/netns/$ns/resolv.conf"
nft add table ip devspace_bash_nat; created_nat=1
nft 'add chain ip devspace_bash_nat postrouting { type nat hook postrouting priority srcnat; policy accept; }'
nft add rule ip devspace_bash_nat postrouting ip saddr 10.203.0.2/32 masquerade
cat <<EOF
Provisioned. Review DNS and allow forwarding for $iface in your EXISTING firewall.
Do NOT disable your firewall. Configuration fragment:
"bashResourceLimit": {
  "enabled": true, "cpuPercent": 25, "memoryMB": 1024,
  "systemdUser": false,
  "bandwidthBytesPerSecond": $rate,
  "networkNamespacePath": "/run/netns/$ns",
  "networkInterface": "$iface"
}
The unprivileged devspace account needs administrator-approved permission to start
and stop these system services. Do not expose an MCP server running as root.
Verify: tc -j qdisc show dev $iface; tc -j filter show dev $iface parent ffff:
Rollback (after stopping jobs):
  ip netns del $ns
  ip link del $iface  # may already be removed with the namespace
  nft delete table ip devspace_bash_nat
  rm /etc/netns/$ns/resolv.conf; rmdir /etc/netns/$ns
Forwarding and pre-existing firewall policy are left unchanged by this script.
EOF
