<script lang="ts">
  import { fetchPlaygroundModels, models } from "../stores/api";
  import {
    fetchListeners,
    updateListeners,
    type ListenerInfo,
  } from "../lib/modelConfigApi";
  import { Button } from "$lib/components/ui/button/index.js";
  import { Input } from "$lib/components/ui/input/index.js";

  // Raw listener entries from the API. Checkbox selections are kept separate
  // (keyed by listen address) so they stay reactive to the models store
  // loading asynchronously.
  let rawListeners = $state<ListenerInfo[]>([]);
  let selections = $state<Record<string, string[]>>({});
  let loadError = $state("");
  let saving = $state(false);
  let saveMessage = $state("");
  let saveError = $state("");
  let filterText = $state("");

  let localModelIds = $derived(
    $models.filter((m) => !m.peerID && m.playgroundType === "model").map((m) => m.id),
  );

  function isGlob(pattern: string): boolean {
    return /[*?]/.test(pattern);
  }

  // Exact configured model IDs that exist in the current model list — these
  // are representable as ticked checkboxes.
  function exactConfigured(l: ListenerInfo): string[] {
    return (l.models ?? []).filter((p) => !isGlob(p) && localModelIds.includes(p));
  }

  // Glob patterns or stale exact IDs that cannot be shown as checkboxes;
  // preserved verbatim on save.
  function legacyPatterns(l: ListenerInfo): string[] {
    return (l.models ?? []).filter((p) => isGlob(p) || !localModelIds.includes(p));
  }

  function selectedFor(l: ListenerInfo): string[] {
    return selections[l.listen] ?? exactConfigured(l);
  }

  function toggleModel(l: ListenerInfo, id: string, checked: boolean) {
    const current = selectedFor(l);
    const next = checked ? [...current, id] : current.filter((s) => s !== id);
    selections = { ...selections, [l.listen]: next };
  }

  function filteredModels(): string[] {
    const needle = filterText.trim().toLowerCase();
    if (!needle) return localModelIds;
    return localModelIds.filter((id) => id.toLowerCase().includes(needle));
  }

  $effect(() => {
    // The checkbox list needs the model catalogue; make sure it is loaded
    // even when this panel mounts before any page fetched it.
    if ($models.length === 0) void fetchPlaygroundModels();
    fetchListeners()
      .then((list) => {
        rawListeners = list;
      })
      .catch((err) => {
        loadError = String(err);
      });
  });

  function addListener() {
    rawListeners = [
      ...rawListeners,
      { name: "", listen: "127.0.0.1:", models: [] },
    ];
  }

  function removeListener(index: number) {
    rawListeners = rawListeners.filter((_, i) => i !== index);
  }

  async function save() {
    if (saving) return;
    saving = true;
    saveMessage = "";
    saveError = "";
    try {
      const payload: ListenerInfo[] = rawListeners.map((l) => ({
        name: l.name.trim(),
        listen: l.listen.trim(),
        // preserved globs first so exact selections remain visible in configs
        models: [...legacyPatterns(l), ...localModelIds.filter((id) => selectedFor(l).includes(id))],
      }));
      await updateListeners(payload);
      saveMessage =
        "Saved. llama-swap is reloading — new ports open in a moment; running models stop and start on their next request.";
      fetchListeners()
        .then((list) => {
          rawListeners = list;
        })
        .catch(() => {});
    } catch (err) {
      saveError = String(err);
    } finally {
      saving = false;
    }
  }
</script>

<div class="rounded-lg border p-4 space-y-3 max-w-2xl mb-4">
  <div class="flex items-center gap-3">
    <h4 class="text-sm font-semibold text-muted-foreground">HTTP Listeners</h4>
    <span class="text-muted-foreground text-xs">
      Each port serves the ticked models. The main <code>-listen</code> address always serves every model.
    </span>
    <Button variant="outline" size="sm" class="ml-auto" onclick={addListener}>Add port</Button>
  </div>

  {#if loadError}
    <p class="text-destructive text-sm">{loadError}</p>
  {:else}
    {#if rawListeners.length === 0}
      <p class="text-muted-foreground text-sm">
        No extra listeners configured. The main <code>-listen</code> address serves every model.
      </p>
    {:else}
      <Input class="max-w-xs" placeholder="Filter models…" bind:value={filterText} />
    {/if}

    {#each rawListeners as l, i (l.listen + String(i))}
      <div class="rounded-md border p-3 space-y-2">
        <div class="flex items-center gap-2">
          <Input class="w-36" placeholder="name" bind:value={l.name} />
          <Input class="w-44 font-mono" placeholder="host:port" bind:value={l.listen} />
          {#if l.bound === true}
            <span class="text-xs text-green-600 dark:text-green-400">bound</span>
          {:else if l.bound === false}
            <span class="text-destructive text-xs" title={l.error}>not bound</span>
          {/if}
          <Button
            variant="ghost"
            size="sm"
            class="ml-auto text-destructive"
            onclick={() => removeListener(i)}>Remove</Button
          >
        </div>

        <div class="bg-background max-h-52 overflow-y-auto rounded-md border">
          {#if $models.length === 0}
            <p class="text-muted-foreground px-3 py-2 text-xs">Loading models…</p>
          {:else if filteredModels().length === 0}
            <p class="text-muted-foreground px-3 py-2 text-xs">No models match this filter.</p>
          {:else}
            <div class="grid grid-cols-1 gap-x-4 p-2 sm:grid-cols-2">
              {#each filteredModels() as id (id)}
                <label class="hover:bg-muted/50 flex cursor-pointer items-center gap-2 rounded px-1.5 py-1">
                  <input
                    type="checkbox"
                    class="accent-primary size-3.5"
                    checked={selectedFor(l).includes(id)}
                    onchange={(e) => toggleModel(l, id, e.currentTarget.checked)}
                  />
                  <span class="truncate font-mono text-xs">{id}</span>
                </label>
              {/each}
            </div>
          {/if}
        </div>

        <p class="text-muted-foreground text-xs">
          {#if selectedFor(l).length === 0 && legacyPatterns(l).length === 0}
            Nothing selected — this port would serve nothing.
          {:else}
            Serves {selectedFor(l).length} ticked model{selectedFor(l).length === 1 ? "" : "s"}
            {#if legacyPatterns(l).length > 0}
              + preserved glob/stale patterns: <span class="font-mono">{legacyPatterns(l).join(", ")}</span>
            {/if}
          {/if}
        </p>
      </div>
    {/each}

    {#if saveMessage}
      <p class="text-xs text-green-600 dark:text-green-400">{saveMessage}</p>
    {:else if saveError}
      <p class="text-destructive text-xs">{saveError}</p>
    {/if}

    {#if rawListeners.length > 0}
      <div class="flex justify-end">
        <Button size="sm" onclick={save} disabled={saving}>
          {saving ? "Saving…" : "Save & Restart"}
        </Button>
      </div>
    {/if}
  {/if}
</div>
