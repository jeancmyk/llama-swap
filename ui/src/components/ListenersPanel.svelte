<script lang="ts">
  import { models } from "../stores/api";
  import {
    fetchListeners,
    updateListeners,
    type ListenerInfo,
  } from "../lib/modelConfigApi";
  import { Button } from "$lib/components/ui/button/index.js";
  import { Input } from "$lib/components/ui/input/index.js";

  interface EditableListener {
    name: string;
    listen: string;
    // exact model IDs ticked by the user via checkboxes
    selected: string[];
    // pre-existing glob patterns (or stale exact IDs) that cannot be
    // represented as checkboxes; preserved verbatim on save
    legacyPatterns: string[];
    bound?: boolean;
    error?: string;
  }

  let listeners = $state<EditableListener[]>([]);
  let loadError = $state("");
  let saving = $state(false);
  let saveMessage = $state("");
  let saveError = $state("");
  let filterText = $state("");

  // local model IDs available for assignment
  let localModelIds = $derived(
    $models.filter((m) => !m.peerID && m.playgroundType === "model").map((m) => m.id),
  );

  function splitPatterns(patterns: string[], knownIds: string[]): { selected: string[]; legacy: string[] } {
    const selected: string[] = [];
    const legacy: string[] = [];
    for (const p of patterns) {
      if (!p) continue;
      const isGlob = /[*?]/.test(p);
      if (!isGlob && knownIds.includes(p)) selected.push(p);
      else legacy.push(p);
    }
    return { selected, legacy };
  }

  function toEditable(l: ListenerInfo): EditableListener {
    const { selected, legacy } = splitPatterns(l.models ?? [], localModelIds);
    return { name: l.name, listen: l.listen, selected, legacyPatterns: legacy, bound: l.bound, error: l.error };
  }

  function filteredModels(): string[] {
    const needle = filterText.trim().toLowerCase();
    if (!needle) return localModelIds;
    return localModelIds.filter((id) => id.toLowerCase().includes(needle));
  }

  function toggleModel(listener: EditableListener, id: string, checked: boolean) {
    if (checked) {
      if (!listener.selected.includes(id)) listener.selected.push(id);
    } else {
      listener.selected = listener.selected.filter((s) => s !== id);
    }
  }

  $effect(() => {
    fetchListeners()
      .then((list) => {
        listeners = list.map(toEditable);
      })
      .catch((err) => {
        loadError = String(err);
      });
  });

  function addListener() {
    listeners = [...listeners, { name: "", listen: "127.0.0.1:", selected: [], legacyPatterns: [] }];
  }

  function removeListener(index: number) {
    listeners = listeners.filter((_, i) => i !== index);
  }

  async function save() {
    if (saving) return;
    saving = true;
    saveMessage = "";
    saveError = "";
    try {
      const payload: ListenerInfo[] = listeners.map((l) => ({
        name: l.name.trim(),
        listen: l.listen.trim(),
        // legacy globs first so exact selections remain visible in configs
        models: [...l.legacyPatterns, ...localModelIds.filter((id) => l.selected.includes(id))],
      }));
      await updateListeners(payload);
      saveMessage =
        "Saved. llama-swap is reloading — new ports open in a moment; running models stop and start on their next request.";
      fetchListeners()
        .then((list) => {
          listeners = list.map(toEditable);
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
    {#if listeners.length === 0}
      <p class="text-muted-foreground text-sm">
        No extra listeners configured. The main <code>-listen</code> address serves every model.
      </p>
    {:else}
      <Input class="max-w-xs" placeholder="Filter models…" bind:value={filterText} />
    {/if}

    {#each listeners as l, i (i)}
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
          {#if filteredModels().length === 0}
            <p class="text-muted-foreground px-3 py-2 text-xs">No models match this filter.</p>
          {:else}
            <div class="grid grid-cols-1 gap-x-4 p-2 sm:grid-cols-2">
              {#each filteredModels() as id (id)}
                <label class="hover:bg-muted/50 flex cursor-pointer items-center gap-2 rounded px-1.5 py-1">
                  <input
                    type="checkbox"
                    class="accent-primary size-3.5"
                    checked={l.selected.includes(id)}
                    onchange={(e) => toggleModel(l, id, e.currentTarget.checked)}
                  />
                  <span class="truncate font-mono text-xs">{id}</span>
                </label>
              {/each}
            </div>
          {/if}
        </div>

        <p class="text-muted-foreground text-xs">
          {#if l.selected.length === 0 && l.legacyPatterns.length === 0}
            Nothing selected — this port would serve nothing.
          {:else}
            Serves {l.selected.length} ticked model{l.selected.length === 1 ? "" : "s"}
            {#if l.legacyPatterns.length > 0}
              + preserved glob/stale patterns: <span class="font-mono">{l.legacyPatterns.join(", ")}</span>
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

    {#if listeners.length > 0}
      <div class="flex justify-end">
        <Button size="sm" onclick={save} disabled={saving}>
          {saving ? "Saving…" : "Save & Restart"}
        </Button>
      </div>
    {/if}
  {/if}
</div>
