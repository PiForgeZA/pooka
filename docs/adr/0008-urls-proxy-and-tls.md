# 0008: Each environment gets an HTTPS URL served by a proxy inside pookad

Status: accepted
Date: 2026-10-21

## Context

Sprites gives every sprite `https://<name>-<org>.sprites.app`. The URL routes to
port 8080 inside the sprite. Only the organization can reach it by default. The
owner can set it to `public`. A request to a stopped sprite wakes it. SPEC section
17 lists this as phase 3. ADR 0004 left the Sprites API on plain HTTP and said that
phase 3 must put TLS in front.

The host sits on a LAN. The user's own DNS (AdGuard) serves `piforge.internal` on
that LAN. That DNS does not help a user on another network, on a client's VPN, or a
second user with a different resolver. pooka cannot fix reachability: a client must
have a route to the host. pooka can fix name resolution.

The host has no public domain. No public CA issues a certificate for a name that
only the LAN can resolve. Ports 80 and 443 on the host are free. AdGuard and Nginx
Proxy Manager run in their own containers with their own addresses.

The `PUT /v1/sprites/{name}` endpoint already accepts `url_settings.auth`. The
container description JSON that `create` writes is the only state store SPEC
section 3 allows.

## Decision

Domain and routing:

- `pookad.yaml` gains `domain`. The URL of an environment is
  `https://<name>-<org>.<domain>`. `pooka setup` defaults `domain` to
  `<host-ip>.sslip.io`, for example `192-168-68-200.sslip.io`. Any public resolver
  maps every name under it to the host. A user who runs a LAN DNS can set a shorter
  domain, for example `pooka.piforge.internal`, with one wildcard record.
- The proxy routes on the first DNS label only. It splits the label into `<name>`
  and `<org>` at the last hyphen and ignores the rest of the host name. So
  `web-local.pooka.piforge.internal` and `web-local.192-168-68-200.sslip.io` reach
  the same environment. A Tailscale or VPN name works the same way without new
  configuration.
- The proxy looks the environment up through `Backend.Get`, the same `pct list`
  and config file path that `List` uses, and caches the answer for 2 seconds. It
  never reads the cluster resources endpoint.
- The proxy forwards to port 8080 in the environment. The port is fixed, as in
  Sprites.

Proxy and TLS:

- The proxy runs inside `pookad`. One binary, one systemd unit, one certificate.
  It listens on `0.0.0.0:443` by default, configured by `proxy_listen`. An empty
  `proxy_listen` disables the proxy.
- On the same listener, the host name `api.<domain>` serves the Sprites API over
  TLS. The plain HTTP listener on 7700 stays for LAN clients and for `pooka setup`.
  An environment named `api` is `api-local.<domain>`, so the two never collide.
- On first start pookad writes a private CA and a wildcard certificate for
  `*.<domain>` and `api.<domain>` under `/etc/pooka/tls/`. It renews the leaf when
  `domain` changes or the leaf has less than 30 days left. `tls_cert` and `tls_key`
  in `pookad.yaml` replace the generated pair for a user who holds a real domain
  and a real certificate.
- `pooka setup` copies the CA certificate to `~/.config/pooka/ca.pem`, points
  `daemon_url` at `https://api.<domain>`, and prints the one-time step that adds
  the CA to the client's trust store. The `pooka` CLI trusts `ca.pem` when the file
  exists. A user who does not trust the CA sees a browser warning and nothing else
  breaks.

Wake on request:

- A request for a stopped environment makes the proxy call `Backend.Start`. One
  start runs per name; concurrent requests wait on it. The proxy then connects to
  port 8080 every 250 ms and forwards the original request on the first success.
  After 30 seconds it answers `502` with a plain text reason. A running environment
  with nothing on port 8080 gets the same `502` at once.

URL auth:

- `url_settings.auth` is `sprite` or `public`. The value lives in the container
  description JSON as `url_auth`. `Env` carries it and `Backend` gains
  `Update(ctx, name, Settings)` to write it. `PUT /v1/sprites/{name}` stores it.
  `pooka url <name> [--auth sprite|public] [--open]` is the CLI side.
- With `sprite`, the proxy accepts either `Authorization: Bearer <token>` or a
  cookie named `pooka_auth` whose value is `HMAC-SHA256(key=token, msg=host)` in
  hex. A request with `?token=<token>` sets the cookie as `Secure` and `HttpOnly`
  and redirects to the same URL without the query parameter. Nothing is stored on
  the host. The cookie is valid for one host name only and never contains the
  token. `pooka url` prints the URL with the token appended so a browser can open
  it once.
- With `public`, the proxy skips the check.

## Consequences

- A user who can reach the host over any network gets working URLs with no DNS
  setup. The default needs internet access from the client to resolve sslip.io.
  A LAN without internet must set a LAN domain.
- The trust boundary moves. With the API on 443 the bearer token no longer travels
  in clear text, which closes the consequence in ADR 0004. The 7700 listener
  remains plain HTTP on the LAN. A user who exposes 443 beyond the LAN exposes
  every `public` environment and the API login surface, with one token as the only
  gate.
- The `Backend` interface and `Env` change. The HTTP backend and the fake backend
  in `backendtest` implement `Update`. SSH transport can write the setting too,
  through `pct set --description`.
- A private CA on the host is state outside Proxmox. It is derivable: delete
  `/etc/pooka/tls/` and pookad makes a new one. Clients then re-trust the CA. This
  is the one exception to "all state in Proxmox" and the reason it is recorded here.
- The proxy needs a TCP connection into the environment. Environments reach the
  host over `vmbr0` today. A future firewall policy must keep host to environment
  port 8080 open.
