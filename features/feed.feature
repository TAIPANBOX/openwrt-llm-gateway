# Where these scenarios come from
#
# The release plan of 2026-11-01: llm-gateway is installed from its own signed apk feed, so a router
# adds the key once and apk then checks every package; with another key or a damaged package it
# refuses. The ownership scenario comes from a defect measured the same day (docs/evidence/
# red-first-feed-2026-11-01.txt): the r1 packages installed /usr/bin/llm-gateway and
# /etc/init.d/llm-gateway owned by nobody. The Pages scenario comes from the first publication, the
# same day, which stopped after its push when GitHub answered 409 to turning Pages on.
#
# Bound to Go tests, or to the checks of scripts/vlab-feed.sh, which installs from the feed on a
# real OpenWrt 25.12 router in the virtual lab:
#   # bound to: TestName[, TestName...]
#   # bound to vlab: check_name[, check_name...]   (names from scripts/vlab-*.sh --selftest)

Feature: llm-gateway installs from its signed feed, and only from it

  # bound to vlab: installs_from_the_signed_index
  Scenario: a router that trusts the feed's key installs llm-gateway from the signed index
    Given the feed's key is in /etc/apk/keys and the feed is in the router's repositories
    When the owner runs "apk add llm-gateway"
    Then the package installs with no --allow-untrusted
    And apk names the feed as where it came from

  # bound to vlab: refused_without_the_key
  Scenario: without the key the feed is not used at all
    Given the feed is in the router's repositories and its key is not in /etc/apk/keys
    When the owner runs "apk add llm-gateway"
    Then apk calls the feed untrusted and finds no such package

  # bound to vlab: refused_with_another_key
  Scenario: a feed signed by another key is refused
    Given a key in /etc/apk/keys that did not sign the feed
    When the owner runs "apk add llm-gateway"
    Then apk calls the feed untrusted and installs nothing

  # bound to vlab: refused_when_the_package_is_corrupted
  Scenario: a damaged package is refused even under a signed index
    Given the feed's key and its signed index, and a package with one byte changed
    When the owner runs "apk add llm-gateway"
    Then apk reports a file integrity error and installs nothing

  # bound to vlab: installed_files_belong_to_root
  Scenario: every file the package installs belongs to root
    Given llm-gateway installed from the feed
    When the owners of its files are listed
    Then every file and every directory it shares with the system is owned by 0:0

Feature: the feed's key

  # bound to: TestFeedSigningStaysOnTheMacUnlessForgeIsAsked
  Scenario: the feed is signed on the Mac that holds the key, unless forge is asked for
    Given the signing container runs on this Mac's own Docker by default
    When the feed is signed
    Then the key reaches the container only on its standard input and never leaves the Mac
    And when that Docker does not answer, signing stops with a message instead of moving elsewhere
    And forge signs only with FEED_SIGN_ON=forge, which the log says

  # bound to: TestFeedPublicKey
  Scenario: the published key is an EC public key on P-256, as apk needs
    Given keys/llm-gateway.pem
    When it is read
    Then it is one PEM public key on prime256v1 and nothing else

  # bound to: TestNoPrivateKeyCommittable
  Scenario: the private key can never be committed
    Given the private half lives only in the keychain of the Mac that signs
    When git lists the files it would commit
    Then none of them holds a private key and none is named *.private.pem

Feature: publishing the feed

  # bound to: TestPagesStepTakesAlreadyEnabledAsOn
  Scenario: publishing goes on when the push has already turned GitHub Pages on
    Given the feed was just pushed to gh-pages and GitHub turned Pages on by itself
    When publish-feed.sh asks GitHub to turn Pages on and gets 409, already enabled
    Then it takes Pages as on, reads the source back until it says gh-pages /
    And goes on to wait until the published key and indexes match the build
    And any other error from GitHub stops it with that error
