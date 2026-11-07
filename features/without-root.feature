# Where these scenarios come from
#
# The daemon leaves root in this step. It stands on the one before it (prepare.feature): root does
# the root work, and the daemon works from what root left. These scenarios come from what a daemon
# reachable from every device in the house could do with root, and from two things procd does without
# a word: it starts a daemon as root when its user does not exist, and it cannot filter a static
# binary at all.
#
# Every scenario is bound by name to Go tests or to a check of the run on a real OpenWrt 25.12 router,
# in the comment line above it:
#   # bound to: TestName[, TestName...]
#   # bound to vlab: check_name[, check_name...]   (names from scripts/vlab-upgrade.sh --selftest)

Feature: the daemon runs as its own user, in a jail, with nothing it does not need

  # bound to: TestInitScriptHardening, TestCapabilitiesFileIsEmpty
  # bound to vlab: daemon_runs_without_root, every_thread_is_confined
  Scenario: the gateway runs under its own account and not as root
    When the service starts
    Then the daemon's user is llm-gateway, with no capability in any set, the bounding set included
    And no_new_privs is set, and every thread is under its seccomp filter

  # bound to: TestInitScriptHardening
  # bound to vlab: ca_key_is_not_in_the_jail
  Scenario: the CA's key never reaches the jail
    When the daemon is running
    Then what it can see holds the certificate and its key, and not the CA's key
    And not dnsmasq's, the firewall's or the network's configuration

  # bound to: TestInitScriptHardening
  # bound to vlab: only_the_journal_is_writable
  Scenario: only the journal can be written
    When the daemon is running
    Then its root and every mount in it are read-only, but the journal's directory

  # bound to: TestServeRefusesRoot, TestConfineComesFirst
  # bound to vlab: root_is_refused_by_hand, root_is_refused_when_procd_keeps_it
  Scenario: a router without the account does not run the gateway as root
    Given procd, told of a user that does not exist, starts the daemon as root
    When the daemon starts
    Then it refuses in one line and listens on nothing

  # bound to: TestCapabilitiesFileIsEmpty
  # bound to vlab: daemon_runs_without_root
  Scenario: all five capability sets are named and empty
    Given procd 2026.03.13 leaves alone a set the file does not name
    When the jail is built
    Then every set is empty, and CapBnd reads as zero

Feature: the daemon filters itself, because procd cannot

  # bound to: TestListNeverAllowsAProgram, TestFilterKillsExec, TestServeNeverExecs
  Scenario: the gateway cannot start a program
    When anything in the daemon tries to run a program
    Then the list has no execve and no execveat, and the kernel ends the process

  # bound to: TestConfineComesFirst, TestFilterCoversEveryThread, TestFilterKillsForbiddenCall, TestFilterAllowsListedCalls
  Scenario: the filter is on before anything is opened, on every thread
    When the daemon starts
    Then after the refusal of root the filter is the first thing it does
    And a thread that was already running is under it too

  # bound to: TestServeRefusesWithoutItsFilter, TestServeNeedsTheKernelToShowItsFilter
  Scenario: a daemon that cannot show its filter does not listen
    Given a kernel that refuses the filter, or a status that does not show it
    When the daemon starts
    Then it refuses in one line

  # bound to: TestFilterOnlyWhereARealRunStandsBehindIt, TestServeSaysWhereItsFilterStaysOff
  Scenario: the filter goes on only where a real run stands behind the list
    Given arm or mips, where no run stands behind the list yet
    When the daemon starts
    Then the filter stays off, and one line says so

  # bound to: TestDaemonRunsUnderItsFilter, TestListHoldsWhatEveryRunNeeds, TestListAllowsTheCopyPaths
  # bound to vlab: no_call_was_refused
  Scenario: the list holds what the daemon does
    When the daemon runs TLS, a backend by name, an https backend, a stream, a client that leaves, and the journal's rotation
    Then no call it makes is refused, and it stops cleanly on SIGTERM

  # bound to: TestUpgradeNeverReachesABackend
  Scenario: nothing here switches protocol
    When a client asks to upgrade the connection
    Then the backend does not hear of it

  # bound to: TestFilterIsRollcallsCopy, TestFilterAllowsOnlyTheList, TestFilterLimits, TestNumbers
  Scenario: the mechanism is rollcall's, on the standard library alone
    When the filter is built
    Then it is rollcall's program, byte for byte, with the list of this daemon

Feature: everything still works without root

  # bound to: TestBackendKeyReachesTheDaemonThroughPrepare
  # bound to vlab: request_reaches_the_backend
  Scenario: a backend's key, 0600 and root's, still reaches the backend
    Given a backend whose key_file only root can read
    When a device's request goes through the jail
    Then the backend gets its key, by a name the router's own DNS answers

  # bound to vlab: stream_and_journal_work_in_the_jail
  Scenario: streams and the journal work in the jail
    When a device asks for a stream, and the journal passes its limit
    Then the stream arrives, and the journal rotates in its directory

  # bound to vlab: reload_keeps_the_daemon_confined
  Scenario: a reload brings a new client in, under the same confinement
    When the owner adds a client and reloads
    Then the new key works, and the new daemon is as confined as the old one

  # bound to vlab: respawn_comes_back_confined
  Scenario: a daemon that dies comes back confined, from the same snapshot
    When the daemon is killed
    Then procd starts it again, without start_service, and it answers
