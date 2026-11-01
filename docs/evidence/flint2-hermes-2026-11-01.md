# Hermes through llm-gateway on a GL.iNet Flint 2, 2026-11-01

A live run on real hardware: Hermes on the router takes its model through llm-gateway
(hermes-openwrt `hermes.main.llm_gateway`, TAIPANBOX/hermes-openwrt#78), then the router is put
back as it was. Times are UTC.

## Router and packages

- GL.iNet Flint 2 (MT7986, aarch64_cortex-a53), OpenWrt 25.12.5 r33051, up 8 days 18 h.
- Installed for the run: llm-gateway 0.1.0-r1 (CI run 41309681745, sha256 d0280760...),
  hermes-agent 0.21.5-r20 and luci-app-hermes 0.21.5-r5 (hermes-openwrt CI run 41315703236 on
  main c4ad750, sha256 f1fb6a5d... and 9cba4b7d...).
- Backend: a stand-in OpenAI server (`e2e/standin.py`) on a house machine, 192.168.1.10:8080,
  model `stand-in`. The gateway listened on 192.168.1.24:8760 (TLS) and 127.0.0.1:8761 only.
- Temperature (cpu-thermal, critical trip 125 C): 38.2 C before, 38.5 to 39.0 C during, 39.1 C
  after.

## Pairing

With `hermes.main.llm_gateway=1` and `hermes.main.model=stand-in`, the hermes-agent init logged:

```
hermes-agent: paired the client hermes with llm-gateway for the model 'stand-in'; its key is in /etc/hermes-agent/llm-gateway.key
hermes-agent: the model 'stand-in' comes from llm-gateway at http://127.0.0.1:8761/v1, as its client hermes; base_url and key_file in /etc/config/hermes are kept and not used while hermes.main.llm_gateway is on
```

`llm-gateway client list` showed `hermes  models stand-in`; the key file was `-rw------- root`;
no file under /etc/config held a key and no process argument did (0 and 0); `base_url` and
`key_file` in /etc/config/hermes were unchanged.

## A model call from the agent

A one-off Hermes job (`hermes cron create 1m ... --repeat 1 --deliver local`), run by the Hermes
gateway process:

- First job (14:49): nine calls through the gateway, each `200`, client `hermes`, backend `forge`,
  `stream: true`; the job failed with "Provider returned an empty stream with no finish_reason".
  Cause: the stand-in answered a streaming request with a plain JSON body and no `finish_reason`.
  The stand-in now streams server-sent events when asked, as model servers do.
- Second job (14:53): `completed ... ok`, response `stand-in answer`; journal line:
  `{"source":"127.0.0.1","client":"hermes","route":"/v1/chat/completions","model":"stand-in","backend":"forge","status":200,"ms":4,"in":12,"out":3,"stream":true}`

At start Hermes also asked the gateway for paths it does not serve (404 before any key check)
and for `/v1/models` with its key (200). The 404s did not stop it; serving `/v1/models/{id}` is
on the list for a later version.

## HTTPS from the LAN

From a laptop on the house LAN (macOS curl, the router's CA as the only trust anchor, `llm.lan`
resolved to 192.168.1.24), a temporary client `mactest`:

| request | answer |
|---|---|
| chat with the key | 200, the stand-in's completion |
| no key | 401 |
| the key, `Origin: https://evil.example` | 403 (`origin`) |
| the key, a model it is not given | 403 (`model`) |
| a name other than llm.lan | TLS refused (the certificate names llm.lan only) |
| plain HTTP to 8760 | 400, nothing served |

The CA carries critical name constraints: `DNS:llm.lan` and the private IPv4 ranges (and fc00::/7).
`mactest` was removed afterwards.

## Put back

`apk del llm-gateway`, hermes-agent 0.21.5-r19 and luci-app-hermes 0.21.5-r4 from the signed feed,
the backed-up /etc/config/{hermes,firewall,dhcp}, both test jobs and their outputs removed, the
gateway's leftover conffile removed. Compared with the state taken before the run: package list,
the md5 of the three configuration files, the three services (hermes-agent, openwrt-mcp,
rollcall running) and the key files are the same; no llm-gateway file, listener, firewall or dhcp
section is left. Hermes is back on its own model through its own provider.
