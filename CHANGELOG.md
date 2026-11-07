# Changelog

## Unreleased: 0.1.0-r3

The first step of taking the daemon off root: everything that must exist before it can drop root.
The daemon itself still runs as root; its user, jail, capabilities and seccomp are the next step.

### Added

- `llm-gateway prepare`, run as root by the init script before anything else: the certificates, a
  snapshot of where to listen and what to answer to, and a copy of the backends' keys, in
  `/var/run/llm-gateway`.
- The account `llm-gateway`, made by the package's scripts and by the init script. Without it the
  service does not start. Removing the package leaves it.
- A `post-upgrade` script in the package: apk runs it, and not `post-install`, when it replaces a
  version. It makes the account, restarts a gateway that runs or is meant to, and removes the old
  journal from `/tmp`. It never enables the service.
- `llm-gateway renew-due` and `/etc/hotplug.d/ntp/50-llm-gateway`: the certificate is renewed within
  30 days of its end on a gateway nobody restarts. `llm-gateway check` shows when it ends.

### Changed

- `llm-gateway serve` works only from what `prepare` left. It starts no program, calls no ubus, reads
  neither `/etc/config/dhcp` nor `/etc/config/firewall` nor a `key_file`, and cannot make or change
  the CA. A snapshot that is missing, damaged or made from another configuration stops it.
- After changing the configuration, `service llm-gateway reload` is needed before a crash, not only
  to take effect: a daemon that respawns onto a changed configuration refuses it.
- `tls.key` is 0640 root:llm-gateway. `ca.key` stays 0600 root.
- The journal moved from `/tmp/llm-gateway` to `/var/run/llm-gateway/journal`. `option journal` can
  name another file in that directory, and nowhere else.
- `port` and `loopback_port` start at 1024. A lower port is refused with one line in the system log,
  before any firewall rule is opened. A router configured below 1024 stops serving after the upgrade
  until the port is changed.

### Fixed

- A failed read of the LAN domain no longer counts as the domain `lan`, which on a router with
  another domain would have made a new CA without a word. A read that fails stops the start and
  changes nothing; a domain that is not set leaves the CA as it is.
- A UCI file whose read fails halfway is no longer taken for a shorter file.
- A CA that is there but cannot be used is no longer replaced. It stops the start, and the log says
  how to start over.
- A new CA is announced in the system log, with the old and the new fingerprint.
- The comment in `mkapk.sh` that said `post-install` covers an upgrade.
