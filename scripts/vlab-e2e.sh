#!/bin/sh
# vlab-e2e.sh -- the end-to-end run on a real OpenWrt firewall, in vlab on forge.
# --selftest lists the checks this script runs, for gate-scenarios-bound.sh.
CHECKS='guest_cannot_reach_the_port lan_cannot_reach_the_model_box lan_reaches_the_model_through_the_gateway models_zone_has_no_internet'
if [ "${1:-}" = "--selftest" ]; then for c in $CHECKS; do echo "$c"; done; exit 0; fi
echo "FAIL: measured nothing: the vlab run is not written yet"; exit 1
