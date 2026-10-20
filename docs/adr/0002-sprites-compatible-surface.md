# 0002: Follow the Sprites vocabulary and API

Status: accepted
Date: 2026-10-20

## Context

Fly publishes the Sprites OpenAPI and AsyncAPI specs. Its SDKs for Go, JavaScript,
Python and Elixir are MIT licensed. Its CLI accepts a custom API URL on login. The
runtime is closed and a promised open-source local runtime has not shipped.

## Decision

pooka uses the Sprites command and concept names: create, exec, console, checkpoint,
restore, stop, start, destroy, services. Phase 2 implements the Sprites OpenAPI spec on a
host daemon so the official clients can target pooka.

## Consequences

- No naming churn when the HTTP API arrives.
- The project may reuse Fly's SDKs instead of writing its own.
- Compatibility of the official CLI auth flow with a third-party server is not verified.
  Test it early in phase 2.
- pooka must not use Fly's trademarks in its own name or domain.
