#!/bin/sh
# Runs on r1: a home LAN on eth2, a guest network on eth3, the models network on eth4. vlab keeps
# OpenWrt's own "lan" interface (eth0) for management, so the house network here is "home".
set -eu
uci -q batch <<'UCI'
set network.home=interface
set network.home.device='eth2'
set network.home.proto='static'
set network.home.ipaddr='192.168.70.1'
set network.home.netmask='255.255.255.0'
set network.guest=interface
set network.guest.device='eth3'
set network.guest.proto='static'
set network.guest.ipaddr='192.168.71.1'
set network.guest.netmask='255.255.255.0'
set network.models=interface
set network.models.device='eth4'
set network.models.proto='static'
set network.models.ipaddr='10.60.0.1'
set network.models.netmask='255.255.255.0'
set dhcp.home=dhcp
set dhcp.home.interface='home'
set dhcp.home.start='100'
set dhcp.home.limit='50'
set dhcp.home.leasetime='1h'
set dhcp.guest=dhcp
set dhcp.guest.interface='guest'
set dhcp.guest.start='100'
set dhcp.guest.limit='50'
set dhcp.guest.leasetime='1h'
set firewall.home=zone
set firewall.home.name='home'
add_list firewall.home.network='home'
set firewall.home.input='ACCEPT'
set firewall.home.output='ACCEPT'
set firewall.home.forward='REJECT'
set firewall.home_wan=forwarding
set firewall.home_wan.src='home'
set firewall.home_wan.dest='wan'
set firewall.guest=zone
set firewall.guest.name='guest'
add_list firewall.guest.network='guest'
set firewall.guest.input='REJECT'
set firewall.guest.output='ACCEPT'
set firewall.guest.forward='REJECT'
set firewall.guest_wan=forwarding
set firewall.guest_wan.src='guest'
set firewall.guest_wan.dest='wan'
set firewall.guest_dhcp=rule
set firewall.guest_dhcp.name='Allow-guest-DHCP'
set firewall.guest_dhcp.src='guest'
set firewall.guest_dhcp.proto='udp'
set firewall.guest_dhcp.dest_port='67'
set firewall.guest_dhcp.target='ACCEPT'
set firewall.guest_dns=rule
set firewall.guest_dns.name='Allow-guest-DNS'
set firewall.guest_dns.src='guest'
set firewall.guest_dns.proto='tcp udp'
set firewall.guest_dns.dest_port='53'
set firewall.guest_dns.target='ACCEPT'
commit
UCI
/etc/init.d/network reload
sleep 6
/etc/init.d/firewall reload >/dev/null 2>&1
/etc/init.d/dnsmasq restart >/dev/null 2>&1

# The package, built on forge by mkapk.sh's pinned apk-tools 3, installed by OpenWrt's own apk.
apk add --allow-untrusted /tmp/s/llm-gateway.apk

llm-gateway models-zone models
uci -q batch <<'UCI'
set llm-gateway.main.enabled='1'
delete llm-gateway.main.zone
add_list llm-gateway.main.zone='home'
set llm-gateway.standin=backend
set llm-gateway.standin.url='http://10.60.0.10:8080/v1'
add_list llm-gateway.standin.model='stand-in'
commit llm-gateway
UCI
llm-gateway client add laptop -model stand-in > /tmp/client.txt
/etc/init.d/llm-gateway restart
sleep 3
echo "setup done"
