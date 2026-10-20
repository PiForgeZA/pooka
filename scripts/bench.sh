#!/usr/bin/env bash
# Measure the phase 1 performance targets from SPEC.md section 15.
# Run ON THE HOST as root:  bash bench.sh
# Creates and destroys a throwaway container tagged pooka-test.
set -euo pipefail

TEMPLATE="${POOKA_TEMPLATE_VMID:-9000}"
VMID="${POOKA_BENCH_VMID:-9199}"

t() { date +%s.%N; }
since() { awk -v a="$1" -v b="$(t)" 'BEGIN{printf "%.2f", b-a}'; }
row() { printf '| %-28s | %6s s |\n' "$1" "$2"; }

if pct status "$VMID" >/dev/null 2>&1; then
  echo "VMID $VMID exists. Pick another with POOKA_BENCH_VMID." >&2; exit 1
fi
trap 'pct stop "$VMID" >/dev/null 2>&1 || true; pct destroy "$VMID" --purge >/dev/null 2>&1 || true' EXIT

echo "| Operation | Time |"
echo "|---|---|"

s=$(t); pct clone "$TEMPLATE" "$VMID" --hostname pooka-bench >/dev/null
pct set "$VMID" --tags pooka-test >/dev/null
row "clone (linked)" "$(since "$s")"

s=$(t); pct start "$VMID"
row "start (cold)" "$(since "$s")"

s=$(t)
for _ in $(seq 1 60); do
  st=$(pct exec "$VMID" -- systemctl is-system-running 2>/dev/null || true)
  case "$st" in running|degraded) break;; esac
  sleep 0.2
done
row "wait systemd ready" "$(since "$s")"

s=$(t); pct exec "$VMID" -- env POOKA_NAME=pooka-bench /etc/pooka/firstboot.sh
row "firstboot" "$(since "$s")"

s=$(t); pct exec "$VMID" -- true
row "exec true" "$(since "$s")"

s=$(t); pct snapshot "$VMID" cp1 --description bench >/dev/null
row "checkpoint" "$(since "$s")"

pct exec "$VMID" -- sh -c 'dd if=/dev/urandom of=/home/pooka/blob bs=1M count=200 status=none'
s=$(t); pct stop "$VMID"; pct rollback "$VMID" cp1 >/dev/null; pct start "$VMID"
row "restore (stop+rollback+start)" "$(since "$s")"

s=$(t); pct stop "$VMID"
row "stop" "$(since "$s")"

s=$(t); pct start "$VMID"
row "start (warm cache)" "$(since "$s")"

s=$(t); pct stop "$VMID"; pct destroy "$VMID" --purge >/dev/null
row "destroy" "$(since "$s")"
trap - EXIT
