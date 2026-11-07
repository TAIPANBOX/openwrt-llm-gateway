# CLAUDE.md

Working rules for this repository. The README is for people who run the gateway; this file says
which properties a change must keep, and which gate or test holds each one. Anything unmarked is
`@claude`. `@decided YYYY-MM-DD` is the owner's decision, paraphrased; never edit those lines.

`@decided 2026-11-01`: everything here is our own code; nothing is forked or vendored from the
portable-LLM projects surveyed in docs/DESIGN.md.
`@decided 2026-11-01`: HTTPS on the LAN by default, at the cost that each device trusts the
router's CA once.

Language: Go, stdlib only. Testing tier: T3 (authorization decisions whose failure is silent).

## Security invariants

1. **No route but `/health` answers without a client key, and a refused request never reaches a
   backend.** Loopback is not trusted.
   (test: `TestRequestWithoutKeyIsRefusedBeforeAnyBackend`, `TestWrongKeyIsRefused`,
   `TestLoopbackRequestStillNeedsAKey`; mutants: empty-key-matches-first-client, any-key-matches,
   loopback-is-trusted)
2. **A client reaches and lists only the models its section gives it.**
   (test: `TestClientReachesOnlyItsModels`, `TestModelListShowsOnlyTheClientsModels`; mutants:
   model-not-checked, model-list-unfiltered, star-not-honoured)
3. **A web page cannot drive a model through a visitor's browser.** No `*` origin ever; an
   `Origin` is refused unless the client lists it; the backend's own CORS headers are stripped;
   a `Host` that is not the gateway's is refused (DNS rebinding).
   (test: `TestNoResponseCarriesAWildcardOrigin`, `TestUnlistedOriginIsRefused`,
   `TestListedOriginIsNamedBack`, `TestForeignHostIsRefused`; gate: `scripts/gate-no-wildcard.sh`;
   mutants: host-not-checked, origin-not-checked, preflight-allows-any-origin,
   backend-cors-passes-through)
4. **It never listens on a wildcard, nor outside the zones it names.** A zone holding wan is
   refused; a public address on a named zone is skipped; a wildcard reported by netifd stops the
   start. The LAN port is TLS only.
   (test: `TestListensOnlyOnNamedZoneAddressesAndLoopback`, `TestOpenWrtZonesReadTheFirewallAndNetifd`,
   `TestServeBindsOnlyTheZoneAddressesAndLoopback`; gate: `scripts/gate-no-wildcard.sh`;
   vlab: `guest_cannot_reach_the_port`; mutants: wildcard-zone-address-bound,
   public-zone-address-bound, wan-zone-accepted, lan-port-plain-http)
5. **The journal never holds a prompt, a completion, a key or a path.** It lives in RAM under
   `/var/run/llm-gateway/journal`, 0600, in a directory of the account's, and nowhere else: `/tmp`
   is open for anyone to put a directory of that name there first.
   (test: `TestJournalRecordsTheRequestWithoutContent`, `TestJournalRecordsARefusalWithoutTheKey`,
   `TestStreamingPassesThroughAndIsRecorded`, `TestJournalLivesUnderVarRun`; vlab:
   `journal_moved_to_var_run`; mutants: journal-keeps-the-offered-key, journal-under-tmp, journal-anywhere)
6. **A model box in the models zone is reachable only from the router**, with no internet unless
   the owner passes `-internet`.
   (test: `TestModelsZoneIsIsolatedByConstruction`, `TestModelsZoneRefusesANetworkThatIsAlreadyZoned`;
   vlab: `lan_cannot_reach_the_model_box`, `lan_reaches_the_model_through_the_gateway`,
   `models_zone_has_no_internet`; mutants: models-zone-takes-a-zoned-network, models-zone-forwards-to-wan)
7. **Nothing leaves the house.** A backend host that is not, or does not resolve only to, a
   private, unique-local, loopback or link-local address is refused at load and again in the
   dialer on every connection. No environment proxy is used.
   (test: `TestPublicBackendAddressIsRefusedAtLoad`, `TestBackendNameResolvingOutsideIsRefusedAtDial`;
   mutants: dial-not-checked, load-not-checked, public-counts-as-house)
8. **The house CA can vouch only for the gateway's names and the private ranges**, critically
   constrained; the gateway's certificate under it renews without changing the CA.
   (test: `TestHouseCACannotVouchForOtherNames`, `TestLeafIsReissuedSilentlyUnderTheSameCA`,
   `TestCertificatesAreCreatedOnceAndKeptPrivate`; mutant: ca-not-name-constrained)
9. **A client key is shown once and stored only as its SHA-256; the client's key never reaches a
   backend.** (test: `TestClientAddShowsTheKeyOnceAndStoresOnlyItsHash`,
   `TestUpstreamKeyReplacesTheClientsKey`; mutants: key-stored-in-clear, client-key-forwarded-upstream)
10. **The feed's signature decides.** Every package and index of the feed verifies against
    `keys/llm-gateway.pem` and no other key; a router with that key installs with no
    `--allow-untrusted`, and without it, with another key, or with a damaged package installs
    nothing. `keys/llm-gateway.pem` is one EC public key on P-256.
    (test: `TestFeedPublicKey`; gate: `scripts/gate-feed.sh`, `scripts/gate-feed-tree.sh`; teeth:
    `scripts/teeth-feed.sh`, the feed section of `scripts/gates-have-teeth.sh`; vlab:
    `refused_without_the_key`, `installs_from_the_signed_index`, `refused_with_another_key`,
    `refused_when_the_package_is_corrupted`; gate mutants: `docs/evidence/feed-gate-mutants-2026-11-01.txt`)
11. **The feed's private key is only in the login keychain of the signing Mac** (service
    `llm-gateway-feed-signing-key`, account `TAIPANBOX/openwrt-llm-gateway`, the PEM in base64). It
    is never a file, a program argument, a CI secret or printed; it reaches the signing container
    only on its standard input, in memory. That container runs on the Mac's own Docker; forge only
    with `FEED_SIGN_ON=forge`, which the log names, and a Mac whose Docker does not answer stops
    rather than moving elsewhere. No private key is committable.
    (partly gated: `TestNoPrivateKeyCommittable`, `check_no_private_key` in `scripts/gate-feed-tree.sh`,
    `TestFeedSigningStaysOnTheMacUnlessForgeIsAsked` with its teeth in `scripts/gates-have-teeth.sh`;
    where the key goes is held by `scripts/feed-keygen.sh` and `scripts/build-feed.sh` as written)
12. **Every entry of a package is root's.** The r1 packages were nobody's.
    (gate: `check_owners` in `mkapk.sh`, run on every CI build; `check_entries_owned_by_root` in
    `scripts/gate-feed.sh`; vlab: `installed_files_belong_to_root`; red first in
    `docs/evidence/red-first-feed-2026-11-01.txt`)
13. **The feed is signed only from what CI built for main.** A successful `ci.yml` run pushed to
    main, for HEAD == `origin/main` on a clean tree, every artifact's `source-tree` equal to HEAD's
    tree. `FEED_TRIAL=1` relaxes the first two for a pull request's run and is never published.
    (partly gated: `scripts/build-feed.sh` and `scripts/publish-feed.sh` refuse otherwise; the
    refusals in `docs/evidence/red-first-feed-2026-11-01.txt`, no teeth case)

14. **The daemon cannot make or change the CA, and nothing but a domain read in full can.** `serve`
    opens only the leaf's two files. `prepare` makes a CA into an empty place, or when a domain it
    read needs one, and says so in one line with both fingerprints. A domain that cannot be read
    stops it; a domain that is not set leaves the CA under the name it was made for; a CA that is
    there and cannot be used stops it, and only someone emptying the place starts over. The CA's
    key stays root's, 0600.
    (test: `TestDaemonLeavesTheCAAlone`, `TestDaemonDoesNotRepairCertificates`, `TestPrepareKeepsTheCA`,
    `TestPrepareRefusesADamagedCA`, `TestPrepareSaysWhenItMakesACA`, `TestPrepareGivesOnlyTheLeafKeyToTheGroup`,
    `TestPrepareWaitsForAnotherPrepare`; gate: `TestServeNeverExecs` with its teeth in
    `scripts/gates-have-teeth.sh`; vlab: `upgrade_kept_the_ca`; mutants: daemon-creates-ca,
    daemon-renews-the-leaf, domain-read-failure-reads-as-lan, half-read-file-reads-as-short,
    no-domain-reads-as-lan, damaged-ca-replaced, lone-ca-key-replaced, new-ca-goes-unsaid,
    ca-key-given-to-the-group, prepare-does-not-lock)
15. **`serve` works only from what root prepared.** The snapshot, `backend.keys`, the leaf and the
    configuration, and nothing else: no program, no ubus, neither the firewall's nor dnsmasq's
    configuration, no `key_file`. Nothing here switches protocol either: `Upgrade` and `Connection`
    do not reach a backend (test: `TestUpgradeNeverReachesABackend`; mutant: upgrade-passes-through). `daemonEnv` has no way to them. A snapshot that is missing,
    damaged, of another version, made from another configuration, or naming anything `ListenAddrs`
    could not have produced stops the start: there is no fallback, because a fallback would need
    root.
    (test: `TestDaemonAsksTheRouterNothing`, `TestServeRefusesAMissingSnapshot`,
    `TestServeRefusesADamagedSnapshot`, `TestServeRefusesAStaleSnapshot`,
    `TestServeRefusesASnapshotThatListensOutside`, `TestBackendKeyReachesTheDaemonThroughPrepare`,
    `TestBackendKeysFileIsReadWithCare`; gate: `TestServeNeverExecs`; vlab: `prepare_left_the_snapshot`;
    mutants: no-config-hash-check, missing-snapshot-reads-as-loopback, snapshot-version-not-checked,
    snapshot-unknown-fields-pass, snapshot-trailing-data-passes, snapshot-hosts-not-checked,
    snapshot-listen-not-checked, snapshot-public-address-listened-on, plain-port-allowed-on-the-lan,
    daemon-calls-netifd, backend-key-read-from-its-file)
16. **A daemon without root cannot change what it listens on.** `/var/run/llm-gateway` is root's,
    0750, its group the account's; the snapshot and `backend.keys` are root's, 0640, that group's,
    and given their owner before they take their name. A daemon that finds either open to more
    than that, or a link in their place, refuses to start. Only `journal/` is the account's.
    (test: `TestPrepareWritesTheSnapshotAndTheKeys`, `TestPrepareOwnsTheSnapshotDirectory`,
    `TestServeRefusesASnapshotOthersCanChange`, `TestPrepareStopsWhenAFileCannotGetItsOwner`; vlab:
    `snapshot_directory_is_roots`, tried as the account; mutants: snapshot-world-readable,
    snapshot-owned-by-the-account, snapshot-directory-open, snapshot-directory-owned-by-the-account,
    snapshot-mode-not-read, link-in-the-directorys-place-followed, wrong-owner-still-takes-the-name,
    leaf-key-stays-root-only)
17. **An upgrade takes effect, and leaves the start at boot as the owner set it.** apk runs
    post-upgrade, never post-install, when it replaces a version, so both are packed, on both ways
    to the package. post-upgrade makes the account, restarts a gateway that runs or is meant to,
    removes the journal of r2 and earlier from `/tmp`, and never enables. The account is made by
    the init script as well, and without it nothing starts.
    (test: `TestPackageRunsPostUpgrade`, `TestInitPreparesBeforeAnythingElse`,
    `TestInitWithoutTheAccountStartsNothing`, `TestPrepareRefusesWithoutTheAccount`; gate:
    `scripts/gate-package-scripts.sh` on every package `mkapk.sh` builds, teeth in
    `scripts/gates-have-teeth.sh`; vlab: `upgrade_ran_post_upgrade`, `upgrade_made_the_account`,
    `upgrade_restarted_the_daemon`, `upgrade_kept_the_configuration`, `gateway_answers_after_the_upgrade`,
    `reboot_brings_it_back`, `fresh_install_makes_the_account`)
18. **No port below 1024, refused in one line before anything is opened.** `prepare` runs before the
    firewall and dnsmasq are touched, and the init script hands its output to the system log.
    (test: `TestPortBelow1024IsRefused`, `TestInitRefusalIsOneLineAndStartsNothing`; vlab:
    `low_port_is_one_line`; mutants: port-below-1024-accepted, port-1024-refused)

19. **The daemon runs as its own user, in a jail, with no capability.** `start_service` gives procd
    the user and group `llm-gateway`, `no_new_privs`, a jail with `requirejail` and no sysfs, and a
    capabilities file with all five sets named and empty, because procd 2026.03.13 (58eb263d) leaves
    alone a set the file does not name, and with no file leaves the bounding set full. The jail mounts
    only what `serve` reads, read-only, with the journal's directory alone writable. `ca.key` and the
    directory that holds it are never in it, and neither are the firewall's, dnsmasq's or the network's
    configuration. `/var/run/llm-gateway` is mounted whole, because ujail makes the parents of a
    mounted file 0755, which the daemon refuses. The package depends on `procd-ujail`.
    (test: `TestInitScriptHardening`, `TestCapabilitiesFileIsEmpty`; gate: `scripts/gate-package-scripts.sh`;
    teeth in `scripts/gates-have-teeth.sh`; vlab: `daemon_runs_without_root`, `every_thread_is_confined`,
    `ca_key_is_not_in_the_jail`, `only_the_journal_is_writable`)
20. **The daemon refuses root, in one line, before anything else.** procd says nothing when the user
    it was told of does not exist, and starts the daemon as root.
    (test: `TestServeRefusesRoot`, `TestConfineComesFirst`; vlab: `root_is_refused_by_hand`,
    `root_is_refused_when_procd_keeps_it`; mutant: serve-as-root-allowed)
21. **The daemon filters itself, first thing, on every thread, and nothing can start a program.** procd
    applies a profile through LD_PRELOAD, which a static binary never loads, so `serve` installs its
    own allow-list (`internal/sandbox`) with TSYNC, right after refusing root, and listens only once
    `/proc/self/status` shows `Seccomp: 2` and `NoNewPrivs: 1`. `execve` and `execveat` are in no list.
    The filter goes on only where a real run stands behind the list, amd64 and arm64 now; elsewhere it
    stays off and one line says so, since a wrong list is a daemon that dies on a rare path. A list for
    another architecture is enabled by running the daemon there, not by editing a build line.
    `internal/sandbox/filter.go` is rollcall's, byte for byte below its package line.
    (test: `TestDaemonRunsUnderItsFilter`, `TestFilterKillsExec`, `TestFilterKillsForbiddenCall`,
    `TestFilterCoversEveryThread`, `TestFilterAllowsListedCalls`, `TestListNeverAllowsAProgram`,
    `TestListHoldsWhatEveryRunNeeds`, `TestListAllowsTheCopyPaths`, `TestListNamesExistPerFamily`,
    `TestFilterOnlyWhereARealRunStandsBehindIt`, `TestFilterIsRollcallsCopy`, `TestServeRefusesWithoutItsFilter`,
    `TestServeNeedsTheKernelToShowItsFilter`, `TestServeSaysWhereItsFilterStaysOff`; gate:
    `scripts/gate-seccomp-run.sh`, in CI on amd64 and arm64, and `TestConfineComesFirst`; vlab:
    `no_call_was_refused`; mutants: filter-failure-ignored, filter-unseen-still-listens, status-never-read,
    filter-off-goes-unsaid, filter-after-the-configuration, no-tsync, thread-left-out-passes,
    no-new-privs-not-set, execve-in-the-list, list-misses-a-needed-call, status-takes-strict-mode,
    unlisted-calls-pass, another-architecture-passes)

## Gates

- `go vet ./... && go test ./...`
- `scripts/gate-scenarios-bound.sh`: every scenario in `features/` bound to tests or vlab checks,
  both ways, and the bound tests run and pass.
- `scripts/gate-no-wildcard.sh`: no wildcard listen address or CORS origin in product code.
- `TestServeNeverExecs`: follows everything `serve` can reach through the package, and fails on a
  program it could start and on any of the root work (`DAEMON_SRC` points it at a planted tree).
- `TestInitPreparesBeforeAnythingElse` and its neighbours run the real init script's `start_service`
  against stand-ins; `TestPackageRunsPostUpgrade` runs the real post-upgrade from `mkapk.sh`.
- `TestConfineComesFirst`: nothing but the refusal of root comes before the filter in `serve`.
- `scripts/gate-package-scripts.sh`: the built package's scripts, its dependency on `procd-ujail` and
  its capabilities file, read back from `apk adbdump`, run by `mkapk.sh` on every package.
- `scripts/gate-seccomp-run.sh`: the real `serve`, static, under its filter on this kernel, then copies
  whose list lacks one call must die by SIGSYS. CI runs it on amd64 and arm64.
- `scripts/gates-have-teeth.sh`: each gate red on its plant, green when clean, "measured nothing"
  without its subject.
- `python3 scripts/mutate.py`: every mutant caught by its named test.
- `scripts/vlab-e2e.sh`: the real firewall, on forge (not in CI; evidence in `docs/evidence/`).
- `scripts/vlab-upgrade.sh`: an older package replaced by this tree's on a real OpenWrt 25.12 router,
  in a QEMU VM of its own on forge: the jail, the kernel's word on the daemon, a request through to a
  backend, a stream, the journal, a reload, a respawn, a reboot, and the certificate's renewal (not in
  CI; evidence in `docs/evidence/`).
- `scripts/gate-feed.sh` and `scripts/teeth-feed.sh`: the signed feed, in the pinned container on
  the Mac's Docker (forge with `FEED_SIGN_ON=forge`), run by `scripts/publish-feed.sh` before every
  push; `scripts/vlab-feed.sh`: the feed on a real router, in vlab on forge.

## Releasing

The feed is signed on the Mac that holds the key, never in CI ([docs/FEED.md](docs/FEED.md)):
`FROM_CI=<ci.yml run id on main> scripts/publish-feed.sh`. It builds nothing: it signs the run's
packages, gates them, and replaces `gh-pages` with one commit holding only the feed. Its Pages step
takes a 409 as Pages already on (the push turns it on) and reads the source back until it says
`gh-pages /` (test: `TestPagesStepTakesAlreadyEnabledAsOn`, teeth in `scripts/gates-have-teeth.sh`). A change to
the package's content raises `PKGREL` in `mkapk.sh`: one name, one content. The feed is replaced
whole, so publishing a new release takes the previous package out of it: keep its `.apk`, since going
back is installing that file (docs/DESIGN.md, Root and the daemon).

A new test is run red first against a stub or a mutant; the red output goes to `docs/evidence/`.
