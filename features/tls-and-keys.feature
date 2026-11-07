# Where these scenarios come from
#
# @decided 2026-11-01: HTTPS on the LAN by default, chosen over plain HTTP with a key, at the cost
# that each client device trusts the router's certificate once. The rest is @claude: a CA the
# device trusts is a root on that device, so it is name-constrained to the gateway; and a key the
# router could show again is a key the router stores, so it shows it once and keeps a hash.
#
# Every scenario is bound by name to Go tests, in the comment line above it:
#   # bound to: TestName[, TestName...]

Feature: the gateway speaks HTTPS on the LAN under a CA of the router's own

  # bound to: TestCertificatesAreCreatedOnceAndKeptPrivate, TestServeBindsOnlyTheZoneAddressesAndLoopback, TestPrepareGivesOnlyTheLeafKeyToTheGroup
  Scenario: the first start makes the certificates and keeps the keys private
    Given a router with no certificates yet
    When the gateway starts
    Then a CA and a certificate for llm.lan and the LAN address exist
    And the CA's key is readable by root only, and the certificate's by root and the gateway's group
    And the LAN port answers HTTPS only, from TLS 1.2 up

  # bound to: TestHouseCACannotVouchForOtherNames
  Scenario: the router's CA cannot vouch for any other site
    Given a device that trusts the router's CA
    And someone who has taken the CA key off the router
    When they sign a certificate for "www.google.com" or for a public address
    Then the device does not accept it

  # bound to: TestLeafIsReissuedSilentlyUnderTheSameCA
  Scenario: the certificate is renewed and follows the LAN address without asking the device again
    Given a device that trusts the router's CA
    When the gateway's certificate nears its end, or the LAN address changes
    Then a new certificate is issued under the same CA
    And the device trusts it without doing anything

Feature: a client's key is shown once and never stored

  # bound to: TestClientAddShowsTheKeyOnceAndStoresOnlyItsHash
  Scenario: adding a client prints its key once and writes only the key's hash
    When the owner adds the client "laptop" for the model "qwen3:14b"
    Then the key, the base URL and the CA fingerprint are printed
    And the configuration holds the key's SHA-256 and not the key

  # bound to: TestClientAddRefusesWhatItCannotWriteSafely
  Scenario: a client that cannot be written safely is not added
    When the owner adds a client whose model contains a quote, or whose name is taken
    Then nothing is written and no key is printed

Feature: one device cannot take the model servers from everyone else

  # bound to: TestClientRateIsHeldPerMinute
  Scenario: a client over its rate is told to wait, and others are not
    Given "laptop" may make 2 requests a minute
    When it makes a third in the same minute
    Then the answer is 429
    And another client is still answered

  # bound to: TestClientConcurrencyIsHeld
  Scenario: a client with a request in flight waits for it before the next
    Given "laptop" may have 1 request in flight
    When it sends a second while the first is running
    Then the answer is 429
