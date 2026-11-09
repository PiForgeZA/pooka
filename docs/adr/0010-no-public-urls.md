# 0010: pooka does not expose environments to the internet

Status: accepted
Date: 2026-11-09

## Context

ADR 0008 gives every environment `https://<name>-<org>.<domain>` on a LAN domain,
served by the proxy inside `pookad` with a certificate from a private CA. The URL
auth setting `public` means "no credential needed". The URL still works only for a
client that can reach the host. SPEC section 17 listed a tunnel as the phase 3b
candidate, so `public` URLs could reach the internet with a real certificate.

A design was written for it on 2026-10-26: Cloudflare Tunnel run by the user, a
second proxy listener that serves only `public` environments, no ACME in pookad.
Three mechanisms were weighed:

| Mechanism | Certificate | Open port at home | Third party in the path |
|---|---|---|---|
| Cloudflare Tunnel | Cloudflare's edge | none | Cloudflare ends TLS and sees traffic |
| Direct: port-forward 443, DNS-01 | pookad, via `tls_cert`/`tls_key` | 443 | none |
| frp on a VPS | the user's problem | none | the VPS |

Each one adds moving parts, a credential on the host, and an internet-facing
surface that anyone can wake and scan. The use cases, inbound webhooks and demo
links, are real but rare. None of them needs pooka to take part.

## Decision

pooka stays a LAN product. It is the offline version of Sprites. Environments get
URLs on the LAN domain and nothing else.

- pooka ships no tunnel code, no public listener, no public domain and no ACME.
- `public` keeps its ADR 0008 meaning: no credential needed on the LAN URL.
- The Sprites API at `api.<domain>` stays on the LAN listener only.
- A user who wants an environment on the internet does that outside pooka, with a
  tunnel or a port-forward of their own in front of the proxy. The existing
  `tls_cert` and `tls_key` keys let them serve a public certificate. That setup,
  and its exposure of every environment and the API behind one token, is theirs.

The tunnel design is dropped, not deferred. A new ADR supersedes this one if the
need returns.

## Consequences

- No new keys in `pookad.yaml`, no new command, no new listener.
- The threat model does not change: the LAN is the trust boundary, as ADR 0004
  states for the daemon and ADR 0008 for the proxy.
- The `sprite` CLI and the SDKs work on the LAN and over a VPN only.
- Phase 3b is removed from the roadmap. Phase 4 follows phase 3.
