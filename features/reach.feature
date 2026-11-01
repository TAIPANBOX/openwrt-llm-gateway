# Where these scenarios come from
#
# No owner's words describe these behaviours beyond one decision: @decided 2026-11-01, everything
# in this project is our own code. The checks exist because of defects measured in the field:
# @measured gh api reads of 25 public portable-LLM repositories, 2026-11-01: model servers bound to
# 0.0.0.0, one with a public tunnel switched on by default, so a model meant for one desk was
# reachable from every network segment and from the internet.
#
# Every scenario is bound by name to Go tests or to a check of the vlab end-to-end run on a real
# OpenWrt firewall, in the comment line above it:
#   # bound to: TestName[, TestName...]
#   # bound to vlab: check_name[, check_name...]   (names from scripts/vlab-e2e.sh --selftest)

Feature: the gateway is reachable only from the zones it names

  # bound to: TestListensOnlyOnNamedZoneAddressesAndLoopback, TestServeBindsOnlyTheZoneAddressesAndLoopback, TestOpenWrtZonesReadTheFirewallAndNetifd
  Scenario: it listens on the addresses of the named zones and on loopback, and nowhere else
    Given the zone "lan" with the address 192.168.8.1 and the zone "guest" with 192.168.9.1
    And the gateway names the zone "lan"
    When the gateway starts
    Then it listens on 192.168.8.1 and on 127.0.0.1
    And it does not listen on 192.168.9.1 or on any wildcard address

  # bound to vlab: guest_cannot_reach_the_port
  Scenario: a guest device cannot reach the port
    Given the gateway names the zone "lan"
    When a device in "guest" connects to the gateway's port
    Then the firewall refuses the connection

Feature: a model box in the models zone is reachable only through the router

  # bound to vlab: lan_cannot_reach_the_model_box, lan_reaches_the_model_through_the_gateway
  Scenario: a LAN device cannot talk to a model box directly
    Given a model box in the zone "models"
    When a device in "lan" connects to the model box's port
    Then the firewall refuses the connection
    And a request through the gateway with a valid key is answered

  # bound to vlab: models_zone_has_no_internet
  Scenario: the models zone has no internet unless the owner turns it on
    Given a model box in the zone "models"
    When it connects to an address on the internet
    Then the firewall refuses the connection

Feature: nothing leaves the house

  # bound to: TestPublicBackendAddressIsRefusedAtLoad
  Scenario: a backend on a public address is refused when the configuration loads
    Given a backend whose URL is "https://api.example.com/v1"
    When the configuration loads
    Then loading fails and names that backend

  # bound to: TestBackendNameResolvingOutsideIsRefusedAtDial
  Scenario: a backend name that later resolves to a public address is refused on connection
    Given a backend "box.lan" that resolved to 10.0.60.10 when the configuration loaded
    And "box.lan" now resolves to 203.0.113.7
    When a client request is forwarded to it
    Then the connection is not made
    And the answer is 502
