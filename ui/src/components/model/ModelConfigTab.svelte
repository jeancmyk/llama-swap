<script lang="ts">
  import type { Model } from "../../lib/types";
  import {
    fetchModelConfig,
    updateModelConfig,
    type ModelConfigInfo,
  } from "../../lib/modelConfigApi";
  import * as Card from "$lib/components/ui/card/index.js";
  import { Button } from "$lib/components/ui/button/index.js";
  import { Textarea } from "$lib/components/ui/textarea/index.js";

  interface Props {
    model: Model;
  }

  let { model }: Props = $props();

  let info = $state<ModelConfigInfo | null>(null);
  let loadError = $state("");
  let editedYaml = $state("");
  let saving = $state(false);
  let saveMessage = $state("");
  let saveError = $state("");

  let dirty = $derived(info !== null && editedYaml !== info.yaml);
  let readOnly = $derived(info !== null && !info.editable);

  $effect(() => {
    const id = model.id;
    info = null;
    editedYaml = "";
    loadError = "";
    saveMessage = "";
    saveError = "";
    fetchModelConfig(id)
      .then((result) => {
        if (model.id === id) {
          info = result;
          editedYaml = result.yaml;
        }
      })
      .catch((err) => {
        if (model.id === id) loadError = String(err);
      });
  });

  async function apply() {
    if (!info || !dirty || saving) return;
    saving = true;
    saveMessage = "";
    saveError = "";
    try {
      await updateModelConfig(model.id, editedYaml);
      info = { ...info, yaml: editedYaml };
      saveMessage =
        "Saved. Configuration is reloading — running models were stopped and will start with the new settings on their next request.";
    } catch (err) {
      saveError = String(err);
    } finally {
      saving = false;
    }
  }

  function revert() {
    if (info) {
      editedYaml = info.yaml;
      saveError = "";
      saveMessage = "";
    }
  }
</script>

<div class="flex min-h-0 flex-col gap-3">
  <Card.Root class="shrink-0 gap-0 overflow-hidden py-0">
    <Card.Header class="flex flex-row items-center gap-3 border-b px-4 py-2">
      <Card.Title class="text-sm font-semibold">Config</Card.Title>
      <span class="text-muted-foreground text-xs">
        Raw YAML for this model block in {info?.source_file ?? "config.yaml"}
      </span>
      <div class="ml-auto flex items-center gap-2">
        {#if dirty && !readOnly}
          <span class="text-warning text-xs">unsaved changes</span>
        {/if}
        <Button variant="outline" size="sm" onclick={revert} disabled={!dirty || saving}>
          Revert
        </Button>
        <Button size="sm" onclick={apply} disabled={!dirty || saving || readOnly}>
          {saving ? "Applying…" : "Apply"}
        </Button>
      </div>
    </Card.Header>

    {#if saveMessage}
      <div class="border-b bg-muted/40 px-4 py-2 text-xs text-green-600 dark:text-green-400">
        {saveMessage}
      </div>
    {:else if saveError}
      <div class="border-b bg-destructive/10 px-4 py-2 text-xs text-destructive">
        {saveError}
      </div>
    {/if}

    <Card.Content class="p-3">
      {#if loadError}
        <p class="text-destructive text-sm">{loadError}</p>
      {:else if !info}
        <p class="text-muted-foreground text-sm">Loading configuration…</p>
      {:else}
        {#if readOnly}
          <p class="text-muted-foreground mb-2 text-xs">{info.reason}</p>
        {:else}
          <p class="text-muted-foreground mb-2 text-xs">
            Editing rewrites this block in the config file. Comments inside the
            block are replaced; comments elsewhere are preserved. Apply triggers
            a hot reload.
          </p>
        {/if}
        <Textarea
          class="min-h-[320px] font-mono text-xs"
          bind:value={editedYaml}
          readonly={readOnly}
          spellcheck="false"
        />
      {/if}
    </Card.Content>
  </Card.Root>
</div>
