# Where these scenarios come from
#
# The gateway leaves root in steps. This step is everything that must exist before the daemon can
# drop it, with the daemon itself still running as root; the user, the jail, the capabilities and
# seccomp come after. These scenarios come from reading what the daemon did as root: a CA a device
# trusts must not change because a file could not be read, and a daemon without root must have no
# way to change where it listens.
#
# Every scenario is bound by name to Go tests or to a check of the vlab upgrade run on a real
# OpenWrt 25.12 router, in the comment line above it:
#   # bound to: TestName[, TestName...]
#   # bound to vlab: check_name[, check_name...]   (names from scripts/vlab-upgrade.sh --selftest)

Feature: root prepares, and the daemon works from what root left

  # bound to: TestPrepareWritesTheSnapshotAndTheKeys, TestPrepareTwiceChangesNothing, TestInitPreparesBeforeAnythingElse
  # bound to vlab: prepare_left_the_snapshot
  Scenario: the root step leaves everything the daemon needs before the daemon starts
    Given an enabled gateway on the zone "lan"
    When the service starts
    Then the account exists and the root step runs before the firewall, dnsmasq and the daemon
    And the snapshot names the configuration's hash, the gateway's name, its addresses and its hosts
    And the backends' keys are copied beside it

  # bound to: TestServeBindsOnlyTheZoneAddressesAndLoopback, TestDaemonAsksTheRouterNothing, TestServeNeverExecs
  Scenario: the daemon asks the router nothing
    Given a snapshot made by the root step
    When the daemon starts
    Then it listens on what the snapshot names
    And it calls no program, no ubus, and reads neither the firewall's nor dnsmasq's configuration

  # bound to: TestServeRefusesAMissingSnapshot, TestServeRefusesADamagedSnapshot, TestServeRefusesAStaleSnapshot, TestAConfigurationThatCannotBeReadStopsBothSteps
  Scenario: a snapshot that is missing, damaged or made from another configuration stops the start
    Given a snapshot that is gone, cut at any byte, of another version, or from before a configuration change
    When the daemon starts
    Then it binds nothing and says why in one line

  # bound to: TestServeRefusesASnapshotThatListensOutside
  Scenario: a snapshot cannot take the daemon outside the house
    Given a snapshot naming a wildcard, a public address, or the plain port on the LAN
    When the daemon starts
    Then it binds nothing

  # bound to: TestPrepareOwnsTheSnapshotDirectory, TestServeRefusesASnapshotOthersCanChange, TestPrepareStopsWhenAFileCannotGetItsOwner
  # bound to vlab: snapshot_directory_is_roots
  Scenario: a daemon without root cannot change what it listens on
    When the root step has run
    Then the snapshot's directory is root's, mode 0750, and its group is the account's
    And the snapshot and the keys are root's, mode 0640, and that group's
    And a file that cannot be given its owner stops the root step, and leaves no snapshot
    And a daemon finding either open to more than that refuses to start

  # bound to: TestPrepareRefusesWithoutTheAccount, TestInitWithoutTheAccountStartsNothing
  Scenario: without the account nothing starts
    Given a router on which the account cannot be made
    When the service starts
    Then the root step does not run and the daemon does not start, and the log says so

  # bound to: TestPrepareDoesNotWaitForDNS, TestPrepareWithNoAddressYetLeavesLoopbackAndSaysSo
  Scenario: the root step runs once, so it does not wait for the network
    Given a boot on which DNS and the LAN address are not there yet
    When the service starts
    Then the root step succeeds, loopback alone when there is no address, and says so
    And the daemon still refuses a backend it cannot resolve, and procd retries it

  # bound to: TestPrepareWaitsForAnotherPrepare
  Scenario: two starts at once make one CA
    Given a reload and an interface coming up together
    When the root step runs twice at once
    Then the second waits for the first

  # bound to: TestLANAddressChangeReachesTheDaemonThroughPrepare
  Scenario: a new LAN address reaches the daemon through the root step
    When the LAN address changes and the service restarts
    Then the snapshot and the certificate name the new address, under the same CA

Feature: the CA a device trusts changes only when it has to

  # bound to: TestDaemonLeavesTheCAAlone, TestDaemonDoesNotRepairCertificates, TestServeNeverExecs
  Scenario: the daemon can neither make nor change the CA
    Given a daemon whose CA files are out of its reach
    When it starts, and when it finds its certificate gone
    Then it writes nothing into the certificates' directory

  # bound to: TestPrepareKeepsTheCA
  Scenario: a domain that cannot be read never leads to a new CA
    Given a router whose LAN domain is not "lan", with a CA made for it
    When the root step cannot read dnsmasq's configuration
    Then it stops, the CA and the snapshot are as they were, and the log names the domain
    And with the file or the option gone the CA stays, under the name it was made for

  # bound to: TestPrepareRefusesADamagedCA
  Scenario: a CA that cannot be used is not replaced behind the owner's back
    Given a CA whose key or certificate is gone, damaged or not its own
    When the root step runs
    Then it stops and rewrites nothing

  # bound to: TestPrepareSaysWhenItMakesACA
  Scenario: a new CA is said out loud
    When the first CA is made, or the owner's new domain needs another
    Then one line names the fingerprint, and for a replacement the old one as well

  # bound to: TestPrepareGivesOnlyTheLeafKeyToTheGroup
  Scenario: the daemon's key is the only key it can be given
    When the root step has run
    Then tls.key is root's and the account's group's, mode 0640
    And ca.key stays root's alone, mode 0600, and the certificates' directory stays 0700

  # bound to: TestRenewDueSaysWhenTheCertificateNearsItsEnd, TestNtpHookRenewsOnlyWhenTheClockCanBeBelieved, TestServeSaysWhenTheCertificateHasEnded
  # bound to vlab: ntp_hook_renews_the_certificate
  Scenario: the certificate is renewed although the daemon no longer does it
    Given a gateway that has run for a year without a restart
    When the clock is synchronised and the certificate is within 30 days of its end
    Then the ntp hook restarts the service, and the root step renews it under the same CA

Feature: an upgrade takes effect, and keeps the devices trusted

  # bound to: TestPackageRunsPostUpgrade
  # bound to vlab: upgrade_ran_post_upgrade, upgrade_made_the_account, upgrade_restarted_the_daemon
  Scenario: an upgrade makes the account and restarts the daemon
    Given a router running the package before this one, as root, with no account
    When apk replaces it
    Then apk runs post-upgrade, the account exists, and the new daemon is running
    And the start at boot is left as the owner set it

  # bound to vlab: upgrade_kept_the_ca, upgrade_kept_the_configuration, gateway_answers_after_the_upgrade
  Scenario: an upgrade from the old package leaves every device trusted
    Given a router with a CA and a client key from the package before this one, on a domain that is not "lan"
    When apk replaces the package
    Then the CA's fingerprint is the same, and the same key is answered over HTTPS under the same CA

  # bound to: TestPackageRunsPostUpgrade
  # bound to vlab: fresh_install_makes_the_account, removal_takes_the_keys_copy_and_keeps_the_account
  Scenario: a fresh install makes the account, and removing the package leaves it
    Given a router that never had the account
    When the package is installed, the gateway enabled, and the package later removed
    Then post-install makes the account and the gateway starts
    And removal takes the copy of the backends' keys from RAM and leaves the account

  # bound to vlab: reboot_brings_it_back
  Scenario: a reboot after the upgrade comes up by itself
    When the router reboots, and RAM with the snapshot in it is gone
    Then the root step makes it again, and the gateway answers

Feature: the journal and the ports a daemon without root can have

  # bound to: TestJournalLivesUnderVarRun, TestPackageRunsPostUpgrade
  # bound to vlab: journal_moved_to_var_run
  Scenario: the journal is under /var/run, in a directory of the account's
    When the daemon records a request
    Then it is in /var/run/llm-gateway/journal, and a journal anywhere else is refused
    And the journal an upgrade leaves in /tmp is gone

  # bound to: TestPortBelow1024IsRefused, TestInitRefusalIsOneLineAndStartsNothing
  # bound to vlab: low_port_is_one_line
  Scenario: a port below 1024 is refused with one line
    Given the owner sets the port to 443
    When the service starts
    Then one line in the log says so, no firewall rule is opened, and nothing respawns
