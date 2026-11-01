# llm-gateway for OpenWrt

The models a house runs on its own machines (Ollama on a desktop, llama-server or LM Studio on a
laptop) usually listen on every address with no key, and often answer any web page with
`Access-Control-Allow-Origin: *`. Every device on the network can use them, and so can any site a
visitor opens in a browser.

llm-gateway puts them behind the router:

- **One name for every model in the house**: `https://llm.lan:8760/v1`, OpenAI-compatible
  (`/v1/models`, `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`, streaming).
- **A key per device**, shown once and stored only as its SHA-256. A device sees and uses only the
  models it is given. Limits per minute and per request in flight.
- **HTTPS under the router's own CA.** A device trusts it once; the CA is name-constrained, so it
  can vouch for the gateway and nothing else.
- **The firewall does the rest.** The gateway listens only in the zones you name. `models-zone`
  puts the model machines into a zone that only the router reaches, with no internet unless you
  ask for it, so the gateway is the only way in.
- **No browser drive-by**: no wildcard origin, origins allowed per device, DNS rebinding refused.
- **Nothing leaves the house**: a backend outside the private ranges is refused, at load and on
  every connection.
- **A journal of who used which model**, in RAM, never holding a prompt, an answer or a key.

One static Go binary, about 7 to 9 MB, no database.

## Install

OpenWrt 25.12 with apk, from the signed feed. On the router, as root: trust the feed's key, check
it, add the feed for the router's own architecture, install.

```sh
wget -O /etc/apk/keys/llm-gateway.pem https://taipanbox.github.io/openwrt-llm-gateway/llm-gateway.pem
sha256sum /etc/apk/keys/llm-gateway.pem
echo "https://taipanbox.github.io/openwrt-llm-gateway/25.12/$(cat /etc/apk/arch)/packages.adb" >> /etc/apk/repositories.d/customfeeds.list
apk update && apk add llm-gateway
```

The sum must be `d5151976fb774d291b4954cce7ea86cf4cf25de6c616c3fbbf05fd5a531457fa`; if it is not,
delete the file and stop. apk then checks the index and the package against this key, with no
`--allow-untrusted`, and refuses a package signed by another key or damaged on the way. Without
the key apk ignores the feed. Architectures: `aarch64_cortex-a53` (GL.iNet Flint 2 and other
Filogic routers), `arm_cortex-a7_neon-vfpv4`, `mipsel_24kc`, `x86_64`. The service does nothing
until it is configured. How the feed is built, signed and checked: [docs/FEED.md](docs/FEED.md).

## Use

```sh
# the network where the model machines live becomes a zone only the router reaches
llm-gateway models-zone models

uci set llm-gateway.desktop=backend
uci set llm-gateway.desktop.url='http://10.60.0.10:11434/v1'
uci add_list llm-gateway.desktop.model='qwen3:14b'
uci set llm-gateway.main.enabled=1
uci commit llm-gateway
service llm-gateway restart

llm-gateway client add laptop -model qwen3:14b     # prints the key once, and the CA fingerprint
llm-gateway cert                                   # the CA a device trusts, once
```

On the device: base URL `https://llm.lan:8760/v1`, the key as the API key, and the router's CA
trusted. Agents on the router itself use `http://127.0.0.1:8761/v1`, with a key as well.

## How it is checked

Every behaviour above is a scenario in [`features/`](features), bound to the tests that prove it;
a gate checks the binding both ways. The security properties are listed with their tests, gates
and mutants in [`CLAUDE.md`](CLAUDE.md). The firewall behaviour is run on a real OpenWrt 25.12
router in a virtual lab: [`scripts/vlab-e2e.sh`](scripts/vlab-e2e.sh), results in
[`docs/evidence/`](docs/evidence). Design: [`docs/DESIGN.md`](docs/DESIGN.md).

## License

MIT
