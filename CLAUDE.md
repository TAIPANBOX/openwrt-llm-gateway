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
5. **The journal never holds a prompt, a completion, a key or a path.** It lives in /tmp, 0600.
   (test: `TestJournalRecordsTheRequestWithoutContent`, `TestJournalRecordsARefusalWithoutTheKey`,
   `TestStreamingPassesThroughAndIsRecorded`; mutant: journal-keeps-the-offered-key)
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

## Gates

- `go vet ./... && go test ./...`
- `scripts/gate-scenarios-bound.sh`: every scenario in `features/` bound to tests or vlab checks,
  both ways, and the bound tests run and pass.
- `scripts/gate-no-wildcard.sh`: no wildcard listen address or CORS origin in product code.
- `scripts/gates-have-teeth.sh`: each gate red on its plant, green when clean, "measured nothing"
  without its subject.
- `python3 scripts/mutate.py`: every mutant caught by its named test.
- `scripts/vlab-e2e.sh`: the real firewall, on forge (not in CI; evidence in `docs/evidence/`).
- `scripts/gate-feed.sh` and `scripts/teeth-feed.sh`: the signed feed, in the pinned container on
  the Mac's Docker (forge with `FEED_SIGN_ON=forge`), run by `scripts/publish-feed.sh` before every
  push; `scripts/vlab-feed.sh`: the feed on a real router, in vlab on forge.

## Releasing

The feed is signed on the Mac that holds the key, never in CI ([docs/FEED.md](docs/FEED.md)):
`FROM_CI=<ci.yml run id on main> scripts/publish-feed.sh`. It builds nothing: it signs the run's
packages, gates them, and replaces `gh-pages` with one commit holding only the feed. A change to
the package's content raises `PKGREL` in `mkapk.sh`: one name, one content.

A new test is run red first against a stub or a mutant; the red output goes to `docs/evidence/`.
