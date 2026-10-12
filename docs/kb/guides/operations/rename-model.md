---
title: Renaming a model
summary: "Renaming a model ID from the config page: what changes, what keeps working, and what does not."
category: guides
tags: [rename, model id, config, url]
config_keys: [models, models.*.name]
updated: 2026-10-12
---

# Renaming a model

A model's **ID** is the key under `models:` and the model's identity everywhere:
its URL (`/upstream/<id>/...`), the `id` in `/v1/models`, activity log entries and
routing references. The optional `name` field is only a friendly label for
listings — changing it never changes the URL.

To change the ID, open the model's **Config** tab and edit **Model ID**. Saving
renames the config key, rewrites exact references and triggers a hot reload:

```yaml
# before                                    # after
models:
  broken-tutu-24b-v2.0-c65536:    →    models:
    cmd: llama-server ...              broken-tutu-24b-v2.0-c32768:
listeners:                               listeners:
  - models: [broken-tutu-24b-v2.0-c65536]  - models: [broken-tutu-24b-v2.0-c32768]
```

**What gets updated:** the `models:` key; exact references in `listeners`, routing
groups, selector targets, profile pins, `hooks.on_startup.preload`, matrix `vars`,
`evict_costs` and set DSLs, and `routing.scheduler.settings.fifo.priority` keys.
**What does not:** glob patterns (`qwen*`), other models' `aliases`, peer configs,
and past activity entries.

A running model stops during the reload and starts under the new ID on its next
request. Clients that still send the old ID get a 404 — if you want the old URL
to keep working, add the old ID to the renamed model's `aliases`.

The new ID must be unique (no model, alias, selector or peer model may use it)
and may contain letters, digits, `-`, `_`, `.`, `:`.
