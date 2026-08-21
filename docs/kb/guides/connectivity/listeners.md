---
title: Scoped listeners on multiple ports
summary: Bind extra HTTP ports, each serving only the models named in its glob patterns.
category: guides
tags: [listeners, ports, binding, scoping]
config_keys: [listeners, listeners.*.listen, listeners.*.models, listeners.*.name]
updated: 2026-10-12
---

# Scoped listeners on multiple ports

By default llama-swap binds one address (`-listen`) and serves every model on
it. The `listeners` block binds additional ports, each restricted to a scoped
set of models:

```yaml
listeners:
  - name: roleplay
    listen: localhost:8091
    models: ["rp-*", "charlie"]
  - listen: 0.0.0.0:8092
    models: ["*"]
```

Each entry opens its own port. `models` is a list of shell-style glob
patterns (`path.Match`) matched against model IDs, aliases, selector IDs and
peer FQNs (`peer:model`); only matching models are reachable on that port, and
requests for anything else get a 404. The primary `-listen` port keeps serving
every model — a listener never takes a model away from it.

`name` is optional (defaults to `port-<port>`) and shows in logs and the
Ports page. An omitted host in `listen` binds all interfaces.

## What goes wrong

- **Port already in use** — startup does not fail; the port is skipped with a
  warning in the log and shows as unbound on the Ports page. Find what holds
  it and pick a free port instead.
- **Duplicate listen addresses or names** are rejected at load time, naming
  the offending entries — the server does not start.
- **A pattern that matches nothing** is legal: the port binds and serves only
  health, UI and API paths. A typo in a glob (`llama-*` vs `llama*`) is the
  usual cause of a new port returning only 404s.
- **Peer models** must be written as `peer:model` in `models` to match.
- Editing the block on a running server hot-reloads: added ports bind,
  removed ports close.
