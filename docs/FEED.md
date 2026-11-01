# The signed apk feed

llm-gateway's packages for OpenWrt 25.12 are served as a signed apk feed from GitHub Pages:
<https://taipanbox.github.io/openwrt-llm-gateway/>. The index and the package of every
architecture are signed, so apk installs them with no `--allow-untrusted` and refuses them without
the key.

## Add it to a router

On the router, as root:

```sh
wget -O /etc/apk/keys/llm-gateway.pem https://taipanbox.github.io/openwrt-llm-gateway/llm-gateway.pem
sha256sum /etc/apk/keys/llm-gateway.pem
echo "https://taipanbox.github.io/openwrt-llm-gateway/25.12/$(cat /etc/apk/arch)/packages.adb" >> /etc/apk/repositories.d/customfeeds.list
apk update && apk add llm-gateway
```

The key is [keys/llm-gateway.pem](../keys/llm-gateway.pem), an EC public key on prime256v1, the
kind OpenWrt's own release keys are. Its SHA-256 is

```
d5151976fb774d291b4954cce7ea86cf4cf25de6c616c3fbbf05fd5a531457fa
```

and `sha256sum` on the router prints the same. Without the key apk drops the feed with a warning
(`UNTRUSTED signature`) and `apk add llm-gateway` says `llm-gateway (no such package)`.

Architectures: `aarch64_cortex-a53`, `arm_cortex-a7_neon-vfpv4`, `mipsel_24kc` and `x86_64`, the
matrix of [ci.yml](../.github/workflows/ci.yml). `aarch64_cortex-a53` has run on a GL.iNet Flint 2,
`x86_64` on OpenWrt 25.12.5 in the virtual lab.

## How a build gets into the feed

```sh
FROM_CI=<run id of ci.yml on main> ./scripts/publish-feed.sh
```

1. **The tree.** The working tree must be clean and HEAD must be `origin/main`.
2. **The packages.** [scripts/build-feed.sh](../scripts/build-feed.sh) builds nothing. It takes
   the four `llm-gateway-<arch>` artifacts of a successful `ci.yml` run pushed to main for HEAD,
   the packages CI built after every gate passed. Each artifact carries `source-tree`, the git
   tree it was built from, and it must be HEAD's tree; a stray file, a missing architecture or two
   versions stop the build.
3. **Signing.** The private key is read from the login keychain of the Mac that signs and reaches
   the alpine container `mkapk.sh` pins only on its standard input. The container runs on that
   Mac's own Docker (`--pull never`, `--network none`, its work space in memory) and is gone when
   the signing ends, so the key never leaves the Mac. If that Docker does not answer, signing stops
   and says so. `FEED_SIGN_ON=forge` runs the same container on forge instead, a fallback that
   sends the key there and says so in the log. Inside, each
   package must be `llm-gateway`, declare its directory's architecture and the version its file
   name says, and give every entry to root, or it is not signed. apk-tools v3 then signs the
   packages, builds and signs each index (packages first: `apk mkndx` refuses a package it cannot
   verify) and verifies every file with this feed's key as the only trusted key.
4. **Gates.** [scripts/gate-feed.sh](../scripts/gate-feed.sh) and
   [scripts/teeth-feed.sh](../scripts/teeth-feed.sh) run before anything is pushed.
5. **Publishing.** The `gh-pages` branch is replaced by one fresh commit holding only the feed,
   with `.nojekyll`; GitHub Pages serves it. The script waits until the published key and every
   published index are byte-identical to the ones it built.

The key is used on one Mac, never in CI. A key held as a repository secret is readable by anyone
who can push a workflow, and this key cannot be revoked: a router that trusts it keeps trusting
anything signed with it until someone deletes the file from `/etc/apk/keys`. The pair was made once,
by [scripts/feed-keygen.sh](../scripts/feed-keygen.sh): the private half went from openssl into
a pipe and from there into the keychain through `security -i`, so it was never a file or a program
argument. `TestNoPrivateKeyCommittable` fails if git would commit any private key.

## What the gates check

[scripts/gate-feed-tree.sh](../scripts/gate-feed-tree.sh), which needs no apk and runs in CI
through `gates-have-teeth.sh`:

| check | what it requires |
| --- | --- |
| `check_feed_present` | an index under `25.12/`, or it measured nothing |
| `check_only_feed_files` | `.nojekyll`, `index.html`, `llm-gateway.pem` and per architecture the index and one package; no `.DS_Store`, no other file, no link |
| `check_nojekyll` | `.nojekyll` at the root |
| `check_published_key` | the feed's `llm-gateway.pem` is `keys/llm-gateway.pem`, byte for byte |
| `check_architectures` | exactly CI's architectures, each with the index and the same package |
| `check_no_private_key` | no file holds a private key |

[scripts/gate-feed.sh](../scripts/gate-feed.sh) runs that first, then apk-tools v3 in the same
pinned container:

| check | what it requires |
| --- | --- |
| `check_signed_by_the_feed_key` | every package and index verifies with `keys/llm-gateway.pem` as the only trusted key |
| `check_arch_matches_its_directory` | each package declares its directory's architecture |
| `check_entries_owned_by_root` | every entry of every package is `root:root` |
| `check_index_lists_the_packages` | each index lists exactly the packages beside it, by the name apk asks for, their architecture and size |

[scripts/teeth-feed.sh](../scripts/teeth-feed.sh) runs `gate-feed.sh` twelve times: on the feed
(pass), on a copy re-signed with a throwaway key published as the feed's (pass), and on ten faults,
each of which must fail with the check it plants: the throwaway signatures under the real key, an
unsigned index, a changed byte, a package of another architecture, an index of another
architecture, an entry owned by nobody, an index over another package, packages renamed to a
version they do not hold, a `.DS_Store`, and no feed at all (measured nothing).

## On a router

[scripts/vlab-feed.sh](../scripts/vlab-feed.sh) copies the `x86_64` part of a built feed into an
OpenWrt 25.12.5 router in the virtual lab and installs from it with OpenWrt's own apk:

| check | what it requires |
| --- | --- |
| `refused_without_the_key` | `apk update` calls the feed UNTRUSTED, `apk add llm-gateway` finds no such package |
| `installs_from_the_signed_index` | with the key, it installs with no `--allow-untrusted`, `apk policy` names the feed, `llm-gateway version` runs |
| `installed_files_belong_to_root` | every installed file is `0:0`, and so are the directories it shares with the system |
| `refused_with_another_key` | another key in its place: UNTRUSTED again, nothing installed |
| `refused_when_the_package_is_corrupted` | the feed's key and signed index, one byte of the package changed: `file integrity error`, nothing installed |

## What has been run

On 2026-11-01, on trial builds (`FEED_TRIAL=1`) from the packages of this repository's pull request
#2, CI runs 41321733358, 41323007732 and 41324655324:

- `build-feed.sh` checked the four artifacts against the tree of HEAD, signed them and verified
  every file against `keys/llm-gateway.pem` alone: on forge for the first two builds, on the Mac's
  own Docker for the third, the default since then
  ([forge](evidence/feed-trial-2026-11-01.txt), [Mac](evidence/feed-trial-local-2026-11-01.txt));
- `gate-feed.sh` passed all 5 checks: 8 files signed by the feed key, 4 packages of the right
  architecture, 52 entries all `root:root`, 4 indexes;
- `teeth-feed.sh`: all 12 teeth held;
- nine mutants of `gate-feed.sh` itself were each caught by a tooth
  ([evidence](evidence/feed-gate-mutants-2026-11-01.txt));
- `vlab-feed.sh` passed all 5 checks on OpenWrt 25.12.5 with apk 3.0.5, once on each build, and
  the lab was taken down clean every time ([run 1](evidence/vlab-feed-2026-11-01-run1.txt),
  [run 2](evidence/vlab-feed-2026-11-01-run2.txt), [run 3](evidence/vlab-feed-2026-11-01-run3.txt),
  signed on the Mac);
- the r1 package of CI run 41319092329, installed in the same lab, put `/usr/bin/llm-gateway`,
  `/etc/init.d/llm-gateway` and `/etc/config/llm-gateway` on the router as `65534:65534`; the
  feed serves r2, with every entry root's ([evidence](evidence/red-first-feed-2026-11-01.txt)).
