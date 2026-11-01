# openwrt-llm-gateway: design

Status: design, 2026-11-01. Nothing built yet. Working name; the package and binary are `llm-gateway`.

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
	list model 'qwen3:14b'          # what this backend serves; empty = ask its /v1/models

config client 'hermes'
	option key_hash 'sha256:...'    # the key itself is shown once, at creation, never stored
	list model '*'
	option rate '30/min'
	option concurrent '2'
```

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
6. **A model box in the `models` zone is reachable only from the router.** The package's fw4
   include rejects forwarding from every other zone into `models`; internet for that zone is off
   unless the owner turns it on (model downloads).
7. **Nothing leaves the house.** No cloud backend type exists in v1. A backend whose host is not,
   or does not resolve only to, a private, link-local or loopback address is refused, checked at
   load and again in the dialer on every connection (a name that later resolves outside is caught).

Gate from the survey: CI fails if the source contains `0.0.0.0`, `[::]:` as a listen address, or
`Access-Control-Allow-Origin` outside the test that asserts its absence, with a teeth case.

## Testing

Tier T3: these are authorization decisions whose failure is silent. Layers 1 to 9 of the house
testing rule apply: red-first, Gherkin in `features/` bound both ways, gates with teeth, mutation
testing of invariants 1 to 4 and 7, coverage reported with it.
Real-firmware checks run in OpenWrt's rootfs container in CI (as openwrt-mcp does), and the
end-to-end run is on vlab: a router VM, a `models` segment with a stand-in OpenAI server, a LAN
client and a guest client.

## Packaging

apk for 25.12 and ipk for 24.10; aarch64, arm, mipsel, x86_64 from one `GOARCH` matrix. Size
target under 8 MB on disk, under 20 MB resident. Published later through our own signed feed,
next to hermes-openwrt. hermes-openwrt gets an option to pair with it the way it pairs with
openwrt-mcp: the package creates the `hermes` client and writes its key into `key_file`.

## Later, not v1

LuCI app (backends, clients with show-once keys, journal). Failover between local backends.
A cloud backend, only per client and only when the owner turns it on. Our own phone model server.
A LuCI helper that runs a model in the admin's browser (WebGPU or Chrome's Prompt API).

## Open decision

HTTPS on the LAN by default (each client trusts the router's certificate once) or plain HTTP with
keys and HTTPS as an option. Loopback stays HTTP either way.
