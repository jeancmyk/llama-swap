<script lang="ts">
  import { models } from "../stores/api";
  import {
    fetchListeners,
    updateListeners,
    globMatches,
    type ListenerInfo,
  } from "../lib/modelConfigApi";
  import { Button } from "$lib/components/ui/button/index.js";
  import { Input } from "$lib/components/ui/input/index.js";
  import { Textarea } from "$lib/components/ui/textarea/index.js";

  interface EditableListener {
    name: string;
    listen: string;
    patternsText: string;
    bound?: boolean;
    error?: string;
  }

  let listeners = $state<EditableListener[]>([]);
  let loadError = $state("");
  let saving = $state(false);
  let saveMessage = $state("");
  let saveError = $state("");

  // local model IDs available for match previews
  let localModelIds = $derived(
    $models.filter((m) => !m.peerID && m.playgroundType === "model").map((m) => m.id),
  );

  function parsePatterns(text: string): string[] {
    return text
      .split(/[\n,]/)
      .map((s) => s.trim())
      .filter((s) => s.length > 0);
  }

  function matchedModels(patternsText: string): string[] {
    const patterns = parsePatterns(patternsText);
    return localModelIds.filter((id) => globMatches(patterns, id));
  }

  function toEditable(l: ListenerInfo): EditableListener {
    return { name: l.name, listen: l.listen, patternsText: l.models.join("\n"), bound: l.bound, error: l.error };
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
    listeners = [...listeners, { name: "", listen: "127.0.0.1:", patternsText: "" }];
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
        models: parsePatterns(l.patternsText),
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
      Each port serves a scoped set of models (globs like <code>cydonia-*</code> allowed)
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
    {/if}

    {#each listeners as l, i (i)}
      {@const matches = matchedModels(l.patternsText)}
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

        <Textarea
          class="min-h-[64px] font-mono text-xs"
          placeholder={"one model pattern per line, e.g.\ncydonia-*\n*safeword*"}
          bind:value={l.patternsText}
          spellcheck="false"
        />

        <p class="text-muted-foreground text-xs">
          {#if parsePatterns(l.patternsText).length === 0}
            No patterns yet — this port would serve nothing.
          {:else if matches.length === 0}
            No configured models match these patterns.
          {:else}
            Serves {matches.length} model{matches.length === 1 ? "" : "s"}:
            {matches.slice(0, 6).join(", ")}{matches.length > 6 ? ", …" : ""}
          {/if}
        </p>
      </div>
    {/each}

    {#if saveMessage}
      <p class="text-xs text-green-600 dark:text-green-400">{saveMessage}</p>
    {:else if saveError}
      <p class="text-destructive text-xs">{saveError}</p>
    {/if}

    <div class="flex justify-end">
      <Button size="sm" onclick={save} disabled={saving}>
        {saving ? "Saving…" : "Save & Restart"}
      </Button>
    </div>
  {/if}
</div>
