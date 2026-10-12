<script lang="ts">
  import { push } from "svelte-spa-router";
  import type { Model } from "../../lib/types";
  import {
    fetchModelConfig,
    renameModel,
    renameModelId,
    updateModelConfig,
    type ModelConfigInfo,
  } from "../../lib/modelConfigApi";
  import { isValidModelID } from "../../lib/modelId";
  import { fetchPlaygroundModels, models } from "../../stores/api";
  import * as Card from "$lib/components/ui/card/index.js";
  import { Button } from "$lib/components/ui/button/index.js";
  import { Input } from "$lib/components/ui/input/index.js";
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

  // model ID (rename) editing state
  let idValue = $state("");
  let idSaving = $state(false);
  let idMessage = $state("");
  let idError = $state("");
  let idValid = $derived(isValidModelID(idValue.trim()));
  let idDirty = $derived(idValue.trim() !== model.id);
  // set after a successful rename; the page moves to the new URL once the
  // model list reports the new ID (or a fallback timeout), so the user never
  // sees a "model not found" flash while llama-swap reloads
  let awaitingRename = $state("");

  // display-name editing state
  let editedName = $state("");
  let renaming = $state(false);
  let renameMessage = $state("");
  let renameError = $state("");

  let dirty = $derived(info !== null && editedYaml !== info.yaml);
  let readOnly = $derived(info !== null && !info.editable);
  let nameDirty = $derived(editedName !== (model.name ?? ""));
  // Selectors and profiles are virtual IDs, not models: renaming the
  // models: key cannot apply to them, so hide the rename field.
  let isPlainModel = $derived(
    !model.peerID && (model.playgroundType === undefined || model.playgroundType === "model"),
  );

  $effect(() => {
    const id = model.id;
    info = null;
    editedYaml = "";
    loadError = "";
    saveMessage = "";
    saveError = "";
    idValue = id;
    idMessage = "";
    idError = "";
    editedName = model.name ?? "";
    renameMessage = "";
    renameError = "";
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

  $effect(() => {
    if (!awaitingRename) return;
    const target = awaitingRename;
    if ($models.some((m) => m.id === target)) {
      awaitingRename = "";
      push(`/models/${encodeURIComponent(target)}`);
      return;
    }
    const timer = setTimeout(() => {
      // the model list did not update; switch over anyway
      awaitingRename = "";
      push(`/models/${encodeURIComponent(target)}`);
    }, 15000);
    return () => clearTimeout(timer);
  });

  async function applyId() {
    if (!idDirty || !idValid || idSaving || awaitingRename) return;
    idSaving = true;
    idMessage = "";
    idError = "";
    const newId = idValue.trim();
    try {
      await renameModelId(model.id, newId);
      idMessage =
        "Renamed. The config key and all exact references are updated — waiting for llama-swap to finish reloading…";
      awaitingRename = newId;
      void fetchPlaygroundModels();
    } catch (err) {
      idError = String(err);
    } finally {
      idSaving = false;
    }
  }

  function revertId() {
    idValue = model.id;
    idMessage = "";
    idError = "";
  }

  async function applyName() {
    if (!nameDirty || renaming) return;
    renaming = true;
    renameMessage = "";
    renameError = "";
    try {
      await renameModel(model.id, editedName);
      renameMessage =
        "Saved. The display name reloads with the config and updates everywhere on the next model list refresh.";
    } catch (err) {
      renameError = String(err);
    } finally {
      renaming = false;
    }
  }

  function revertName() {
    editedName = model.name ?? "";
    renameMessage = "";
    renameError = "";
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
  {#if !model.peerID}
    {#if isPlainModel}
      <Card.Root class="shrink-0 gap-0 overflow-hidden py-0">
        <Card.Header class="flex flex-row items-center gap-3 border-b px-4 py-2">
          <Card.Title class="text-sm font-semibold">Model ID</Card.Title>
          <span class="text-muted-foreground text-xs">
            The model's identity: its URL, the /v1/models id and the config key.
          </span>
          <div class="ml-auto flex items-center gap-2">
            {#if idDirty && !readOnly}
              <span class="text-warning text-xs">unsaved changes</span>
            {/if}
            <Button variant="outline" size="sm" onclick={revertId} disabled={!idDirty || idSaving}>
              Revert
            </Button>
            <Button size="sm" onclick={applyId} disabled={!idDirty || !idValid || idSaving || readOnly}>
              {idSaving ? "Renaming…" : "Rename"}
            </Button>
          </div>
        </Card.Header>

        {#if idMessage}
          <div class="border-b bg-muted/40 px-4 py-2 text-xs text-green-600 dark:text-green-400">
            {idMessage}
          </div>
        {:else if idError}
          <div class="border-b bg-destructive/10 px-4 py-2 text-xs text-destructive">
            {idError}
          </div>
        {/if}

        <Card.Content class="p-3">
          {#if readOnly}
            <p class="text-muted-foreground mb-2 text-xs">{info?.reason}</p>
          {/if}
          {#if idValue && !idValid}
            <p class="text-destructive mb-2 text-xs">
              Use letters, digits, '-', '_', '.', ':' — must start alphanumeric.
            </p>
          {/if}
          <Input
            bind:value={idValue}
            class="font-mono text-xs"
            maxlength={128}
            disabled={readOnly}
            spellcheck="false"
          />
          <p class="text-muted-foreground mt-2 text-xs">
            Renaming updates the config key and every exact reference (listeners,
            groups, selectors, profiles, preloads, matrix), then triggers a
            reload. Glob patterns are left alone.
          </p>
        </Card.Content>
      </Card.Root>
    {/if}

    <Card.Root class="shrink-0 gap-0 overflow-hidden py-0">
      <Card.Header class="flex flex-row items-center gap-3 border-b px-4 py-2">
        <Card.Title class="text-sm font-semibold">Display name</Card.Title>
        <span class="text-muted-foreground text-xs">
          Friendly label shown in the UI and /v1/models. Leave empty to use the
          model ID.
        </span>
        <div class="ml-auto flex items-center gap-2">
          {#if nameDirty && !readOnly}
            <span class="text-warning text-xs">unsaved changes</span>
          {/if}
          <Button variant="outline" size="sm" onclick={revertName} disabled={!nameDirty || renaming}>
            Revert
          </Button>
          <Button size="sm" onclick={applyName} disabled={!nameDirty || renaming || readOnly}>
            {renaming ? "Saving…" : "Save"}
          </Button>
        </div>
      </Card.Header>

      {#if renameMessage}
        <div class="border-b bg-muted/40 px-4 py-2 text-xs text-green-600 dark:text-green-400">
          {renameMessage}
        </div>
      {:else if renameError}
        <div class="border-b bg-destructive/10 px-4 py-2 text-xs text-destructive">
          {renameError}
        </div>
      {/if}

      <Card.Content class="p-3">
        {#if readOnly}
          <p class="text-muted-foreground mb-2 text-xs">{info?.reason}</p>
        {/if}
        <Input
          bind:value={editedName}
          placeholder={model.id}
          maxlength={200}
          disabled={readOnly}
          spellcheck="false"
        />
      </Card.Content>
    </Card.Root>
  {/if}

  <Card.Root class="min-h-0 gap-0 overflow-hidden py-0">
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
