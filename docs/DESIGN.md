# openwrt-llm-gateway: design

Status: released 2026-11-01, `llm-gateway` 0.1.0-r2 in its own signed apk feed ([FEED.md](FEED.md)).
The package and the binary are `llm-gateway`.

Provenance: anything unmarked is `@claude` (my reading or plan, re-check before relying on it).
`@measured <how> <date>` is a run. `@decided <date>` is the owner's decision, paraphrased.

## Why

Homes now run models on their own machines: Ollama on a desktop, llama-server or LM Studio on a
laptop, a model app on a phone. The tools that set these up make them reachable the cheap way.

`@measured gh api reads of 25 public repositories of a popular portable-LLM author, 2026-11-01`:
- a chat server bound to `0.0.0.0` with `Access-Control-Allow-Origin: *`, no client key, proxying
  on to Ollama: every device on the network and every web page a visitor opens can drive the model;
- a phone model server bound to `0.0.0.0`, CORS any host, no key on `/v1/chat/completions`, and a
  public Cloudflare/ngrok tunnel switched on by default;
- an API key documented in a README that no handler ever read;
- `llama-server --host 0.0.0.0` with no `--api-key`;
- users asking in issues for exactly this: LM Studio reachable over the LAN, `OLLAMA_HOST=0.0.0.0`,
  `/v1/embeddings` on the local API.

`@measured gh search repos + gh search code in openwrt/packages, 2026-11-01`: no OpenWrt package,
LuCI app or third-party feed for an LLM gateway exists.

The router already knows what such a gateway needs: which zone a device is in, its DHCP name, and
the firewall between zones. So the router is where the policy belongs.

`@decided 2026-11-01`: everything here is our own code. Nothing is forked or vendored from the
projects surveyed above; they are a list of mistakes not to repeat.

## What it is

One static Go binary on OpenWrt (`CGO_ENABLED=0`, stdlib `net/http` and `httputil.ReverseProxy`,
no database), configured by UCI, run by procd. It speaks the OpenAI protocol on one side and
forwards to model servers on the other.

```
 laptop (Claude Code) ─┐                      ┌─> desktop: Ollama   10.0.60.10:11434
 phone app ────────────┼─ https://llm.lan:8760 ┤
 Hermes on the router ─┘   (key per client)    └─> laptop: llama-server 10.0.60.11:8080
       (127.0.0.1:8761, key still required)        zone "models": reachable only from the router
```

Routes: `GET /v1/models`, `POST /v1/chat/completions`, `POST /v1/completions`,
`POST /v1/embeddings`, streaming (SSE) passed through with immediate flush. `GET /health` answers
`ok` and nothing else. Every other path is 404.

## Configuration (UCI `/etc/config/llm-gateway`)

```
config gateway 'main'
	option enabled '0'
	list zone 'lan'                 # zones whose interface addresses it listens on
	option port '8760'              # HTTPS on those addresses
	option loopback_port '8761'     # plain HTTP on 127.0.0.1 only, for agents on the router
	option name 'llm'               # dnsmasq answers llm.<domain> with the LAN address

config backend 'desktop'
	option url 'http://10.0.60.10:11434/v1'
	option key_file ''              # an upstream key, if the model server has one (0600 root)
	list model 'qwen3:14b'          # what this backend serves; listed, never discovered

config client 'hermes'
	option key_hash 'sha256:...'    # the key itself is shown once, at creation, never stored
	list model '*'
	option rate '30/min'
	option concurrent '2'
```

Models are listed per backend, never discovered from its `/v1/models`: a model the owner did not
name never appears, and each model is served by exactly one backend (a second is a configuration
error). The journal is `/tmp/llm-gateway/journal.log` (RAM, 0600, one rotation at 1 MiB), so the
flash takes none of its writes.

Keys: 32 random bytes, base64url, prefix `llmgw_`. Stored as SHA-256 (a high-entropy token needs no
slow hash). Compared with `crypto/subtle`. `llm-gateway client add NAME` prints the key, the
base URL and the certificate fingerprint once.

## Security invariants (each to be held by a named test or gate)

1. **No route but `/health` answers without a client key**, and a refused request never reaches a
   backend. Loopback is not trusted: any process on the router, the agent included, can reach it.
2. **A client reaches only the models its section lists.** `/v1/models` shows it only those.
3. **A web page cannot drive a model through a visitor's browser.** A request carrying an `Origin`
   is refused unless that origin is listed for that client (`list origin`, empty by default), and
   only then does the response name that one origin; there is never a `*`. A request whose `Host`
   is not the gateway's name or one of its addresses is refused (DNS rebinding).
4. **It never listens on a wildcard address.** Only on the addresses of the zones it names, plus
   `127.0.0.1` for the loopback port. A zone it does not name (guest, wan) gets nothing, and its
   fw4 rule opens the port only from the named zones.
5. **The journal never holds a prompt, a completion or a key.** Per request: time, client, model,
   backend, status, latency, token counts from `usage`.
6. **A model box in the `models` zone is reachable only from the router.** `llm-gateway
   models-zone NETWORK` makes that zone: input refused but DHCP and DNS, forward refused, and no
   forwarding into or out of it, so every other zone is rejected on the way in; internet for the
   zone comes only with `-internet` (model downloads).
7. **Nothing leaves the house.** Every backend is in the house. A backend whose host is not,
   or does not resolve only to, a private, link-local or loopback address is refused, checked at
   load and again in the dialer on every connection (a name that later resolves outside is caught).

Gate from the survey: CI fails if the source contains `0.0.0.0`, `[::]:` as a listen address, or
`Access-Control-Allow-Origin` outside the test that asserts its absence, with a teeth case.

## Testing

Tier T3: these are authorization decisions whose failure is silent. Layers 1 to 9 of the house
testing rule apply: red-first, Gherkin in `features/` bound both ways, gates with teeth, mutation
testing of invariants 1 to 4 and 7, coverage reported with it.
`@measured docs/evidence/ 2026-11-01`: every test red first against a stub or a mutant, then
green; 26 of 26 mutants caught; on OpenWrt 25.12.5 in vlab (a router VM, a `models` segment with
a stand-in OpenAI server, a LAN client and a guest client) the four firewall checks passed in
each of four runs and the signed feed installed with OpenWrt's own apk, 5 of 5 checks; on a
GL.iNet Flint 2, Hermes paired itself and answered through the gateway, and HTTPS from a laptop
on the LAN answered 200 with a key, 401 without one ([flint2-hermes-2026-11-01.md](evidence/flint2-hermes-2026-11-01.md)).

## Commands

`serve`, `client add|remove|list`, `cert`, `check`, `version`, and two the init script uses:
`addresses` (where `serve` would listen; the first IPv4 one becomes `llm.<domain>` in dnsmasq,
so the name never points where the gateway does not listen) and `models-zone NETWORK [-internet]`
(invariant 6: a zone with input refused but DHCP and DNS, no forwarding in or out).

## Packaging

apk for OpenWrt 25.12 on `aarch64_cortex-a53`, `arm_cortex-a7_neon-vfpv4`, `mipsel_24kc` and
`x86_64`, from one `GOARCH` matrix in CI. `@measured go build -trimpath -ldflags "-s -w" 2026-11-01`:
the binary is 7.1 MiB on arm64, 7.6 MiB on arm, 7.7 MiB on amd64 and 8.5 MiB on mipsle; each package
is about 3 MB. The apk is written by `apk mkpkg` from apk-tools 3 in a pinned alpine image (OpenWrt's
own apk 3.0.5 ships without `mkpkg`), and published through its own signed feed on GitHub Pages,
signed on the Mac that holds the key, in its own Docker, from the packages CI built:
[FEED.md](FEED.md). Every entry of the package is root's. The first build, r1, named them
nobody:nobody (`@measured scripts/vlab-feed.sh OLD_APK=<r1> 2026-11-01`: `/usr/bin/llm-gateway` and
`/etc/init.d/llm-gateway` installed as 65534:65534 on OpenWrt 25.12.5); it was never published,
and the release is r2.

hermes-openwrt r20 pairs with it the way it pairs with openwrt-mcp: with `hermes.main.llm_gateway`
on, the package creates the `hermes` client and keeps its key in `/etc/hermes-agent/llm-gateway.key`
(0600 root).

## TLS

`@decided 2026-11-01`: HTTPS on the LAN by default, chosen over plain HTTP with a key, at the cost
that each client device trusts the router's certificate once.

On first start the package makes a house CA (ECDSA P-256, `/etc/llm-gateway/ca.key` 0600 root,
`ca.crt`) whose name constraints, marked critical, permit only `llm.<domain>` and the private
ranges, and under it the gateway's certificate (`tls.key` 0600 root, `tls.crt`) for `llm.<domain>`
and the router's LAN addresses. The certificate lives 397 days and is renewed 30 days before its
end, and reissued when the LAN address changes, always under the same CA, so a device trusts the
CA once. `llm-gateway cert` prints the CA and its SHA-256 fingerprint, and `client add` prints the
fingerprint next to the key. TLS 1.2 is the floor. The loopback port stays plain HTTP: it never
leaves the router, and the key is required on it too (invariant 1).
