#!/usr/bin/env bash
# End-to-end test of system-wide mode on Linux (CI runs it on ubuntu-latest). Needs sudo and
# internet access. A machine with one NIC gets a second "uplink" so balancing can be checked:
# a veth pair into a network namespace that NATs back out through the real NIC.
#
#   host nmup-h 10.77.0.1  <->  ns nmup-n 10.77.0.2   (the second uplink net-merge balances over)
#   ns   nmgw-n 10.88.0.2  <->  host nmgw-h 10.88.0.1 (the namespace's way out, NATed by the host)
#
# usage: scripts/linux-e2e.sh ./net-merge
set -uo pipefail
BIN=$(realpath "${1:-./net-merge}")
STATE=/tmp/net-merge-e2e
# Large files on CDNs. A runner sometimes gets nothing from one of them, so the next is tried.
URLS=(https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb
      https://archive.ubuntu.com/ubuntu/dists/noble/Contents-amd64.gz
      https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb)
REAL=$(ip -4 route show default | awk '{print $5; exit}')
fails=0
pass() { echo "PASS  $*"; }
fail() { echo "FAIL  $*"; fails=$((fails + 1)); }

cleanup() {
    sudo pkill -f "$BIN run" 2>/dev/null
    sudo ip netns del nmup 2>/dev/null
    sudo ip link del nmup-h 2>/dev/null
    sudo ip link del nmgw-h 2>/dev/null
    sudo iptables -t nat -D POSTROUTING -s 10.88.0.0/24 -o "$REAL" -j MASQUERADE 2>/dev/null
    sudo iptables -D FORWARD -i nmgw-h -j ACCEPT 2>/dev/null
    sudo iptables -D FORWARD -o nmgw-h -j ACCEPT 2>/dev/null
}
trap cleanup EXIT

wait_on() { # wait until `net-merge status` reports ON
    for _ in $(seq 1 40); do
        "$BIN" status -state-dir "$STATE" 2>/dev/null | grep -q 'is ON' && return 0
        sleep 0.5
    done
    return 1
}
nic_bytes() { # bytes net-merge relayed through an adapter, from its status file
    jq -r --arg n "$1" '.NICs[] | select(.Name == $n) | .BytesIn' "$STATE/status.json"
}

echo "== real uplink: $REAL"
rm -rf "$STATE" && mkdir -p "$STATE"

echo "== building the second uplink"
sudo ip netns add nmup
sudo ip link add nmup-h type veth peer name nmup-n netns nmup
sudo ip link add nmgw-h type veth peer name nmgw-n netns nmup
sudo ip addr add 10.77.0.1/24 dev nmup-h && sudo ip link set nmup-h up
sudo ip addr add 10.88.0.1/24 dev nmgw-h && sudo ip link set nmgw-h up
sudo ip netns exec nmup sh -ec '
    ip link set lo up
    ip addr add 10.77.0.2/24 dev nmup-n && ip link set nmup-n up
    ip addr add 10.88.0.2/24 dev nmgw-n && ip link set nmgw-n up
    ip route add default via 10.88.0.1
    sysctl -qw net.ipv4.ip_forward=1 net.ipv4.conf.all.rp_filter=0
    iptables -t nat -A POSTROUTING -o nmgw-n -j MASQUERADE'
sudo sysctl -qw net.ipv4.ip_forward=1 net.ipv4.conf.all.rp_filter=0 net.ipv4.conf.nmup-h.rp_filter=0
sudo iptables -t nat -A POSTROUTING -s 10.88.0.0/24 -o "$REAL" -j MASQUERADE
sudo iptables -I FORWARD -i nmgw-h -j ACCEPT
sudo iptables -I FORWARD -o nmgw-h -j ACCEPT
sudo ip route add default via 10.77.0.2 dev nmup-h metric 900
ip=$(curl -4 -s --max-time 10 --interface nmup-h https://api.ipify.org) &&
    pass "second uplink reaches the internet on its own (exit IP $ip)" || fail "second uplink has no internet"

echo "== adapter detection (the veth is virtual, so it is used only via -include)"
"$BIN" list

echo "== system-wide ON over $REAL + nmup-h"
sudo "$BIN" run -include "$REAL,nmup-h" -state-dir "$STATE" -log "$STATE/net-merge.log" -stats 0 -for 170s &
wait_on && pass "net-merge turned on" || { fail "net-merge did not turn on"; cat "$STATE/net-merge.log"; exit 1; }
sleep 4 # let the internet probes finish
"$BIN" status -state-dir "$STATE"

routes=$(ip -4 route show dev net-merge)
grep -q '0.0.0.0/1' <<<"$routes" && grep -q '128.0.0.0/1' <<<"$routes" &&
    pass "capture routes 0.0.0.0/1 + 128.0.0.0/1 on net-merge" || fail "capture routes missing: $routes"
mtu=$(cat /sys/class/net/net-merge/mtu)
[ "$mtu" = 9000 ] && pass "TUN MTU is 9000" || fail "TUN MTU is $mtu, want 9000"

code=$(curl -4 -s --max-time 10 -o /dev/null -w '%{http_code}' https://example.com)
[ "$code" = 200 ] && pass "plain HTTPS works through net-merge" || fail "HTTPS through net-merge: $code"
if command -v dig >/dev/null; then
    dig +short +time=3 @1.1.1.1 example.com | grep -qE '^[0-9.]+$' &&
        pass "UDP DNS to 1.1.1.1 works through net-merge" || fail "UDP DNS through net-merge"
fi

echo "== 12 parallel downloads: both uplinks must carry traffic"
for url in "${URLS[@]}"; do
    r0=$(nic_bytes "$REAL"); v0=$(nic_bytes nmup-h)
    pids=()
    for _ in $(seq 1 12); do
        curl -4 -s --max-time 40 -r 0-9999999 -o /dev/null "$url" &
        pids+=($!)
    done
    wait "${pids[@]}"
    sleep 2 # one status tick
    r1=$(nic_bytes "$REAL"); v1=$(nic_bytes nmup-h)
    real_mb=$(((r1 - r0) / 1000000)); veth_mb=$(((v1 - v0) / 1000000))
    echo "   $url: via $REAL: ${real_mb} MB, via nmup-h: ${veth_mb} MB"
    [ $((real_mb + veth_mb)) -ge 10 ] && break # something came through; only then is a bad split a real failure
done
[ "$real_mb" -ge 10 ] && [ "$veth_mb" -ge 10 ] &&
    pass "downloads were split across both uplinks" || fail "downloads not split (real ${real_mb} MB, veth ${veth_mb} MB)"

echo "== off: adapter and routes go away, internet still works"
"$BIN" off -state-dir "$STATE"
sleep 1
ip link show net-merge >/dev/null 2>&1 && fail "net-merge adapter still exists after off" || pass "adapter removed after off"
code=$(curl -4 -s --max-time 10 -o /dev/null -w '%{http_code}' https://example.com)
[ "$code" = 200 ] && pass "internet works after off" || fail "internet after off: $code"

echo "== crash: kill -9 must not leave the machine without internet"
sudo "$BIN" run -include "$REAL,nmup-h" -state-dir "$STATE" -log "$STATE/net-merge-2.log" -stats 0 -for 120s &
wait_on || fail "second start did not turn on"
pid=$(jq -r .PID "$STATE/status.json")
sudo kill -9 "$pid"
sleep 1
ip link show net-merge >/dev/null 2>&1 && fail "adapter survived kill -9" || pass "adapter gone after kill -9"
ip -4 route | grep -q 'dev net-merge' && fail "routes survived kill -9" || pass "routes gone after kill -9"
code=$(curl -4 -s --max-time 10 -o /dev/null -w '%{http_code}' https://example.com)
[ "$code" = 200 ] && pass "internet works after kill -9" || fail "internet after kill -9: $code"

echo
[ "$fails" -eq 0 ] && echo "ALL CHECKS PASSED" || echo "$fails CHECK(S) FAILED"
exit "$fails"
