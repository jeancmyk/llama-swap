<script lang="ts">
  import { push } from "svelte-spa-router";
  import type { Model } from "../../lib/types";
  import {
    acceptModelCapabilities,
    discardModelCapabilities,
    fetchModelConfig,
    renameModel,
    renameModelId,
    triggerReload,
    updateModelConfig,
    type ModelConfigInfo,
  } from "../../lib/modelConfigApi";
  import { isAutoMarkerLine } from "../../lib/capabilityMarker";
  import { isValidModelID } from "../../lib/modelId";
  import { configDirty, fetchPlaygroundModels, models } from "../../stores/api";
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

  // pending capabilities: auto-filled by discovery, awaiting keep/discard
  let accepting = $state(false);
  let discarding = $state(false);
  let pendingBusy = $derived(accepting || discarding);
  let pendingMessage = $state("");
  let pendingError = $state("");
  // applying saved (not yet active) changes
  let applying = $state(false);
  let applyMessage = $state("");

  // backdrop lines for highlighting the auto marker lines in the YAML editor
  let editorEl = $state<HTMLTextAreaElement | null>(null);
  let backdropEl = $state<HTMLElement | null>(null);
  const yamlLines = $derived(editedYaml.split("\n"));
  const markerLineFlags = $derived(yamlLines.map((l) => isAutoMarkerLine(l)));
  const hasMarkerLine = $derived(markerLineFlags.some(Boolean));
  // Backdrop text per line, newline-terminated (except the last) so the pre
  // renders exactly the textarea's lines. (The comparison must live here:
  // Svelte markup expressions cannot contain "<".)
  const backdropLines = $derived(yamlLines.map((l, i) => l + (i < yamlLines.length - 1 ? "\n" : "")));
  function syncBackdropScroll() {
    if (editorEl && backdropEl) {
      backdropEl.scrollTop = editorEl.scrollTop;
      backdropEl.scrollLeft = editorEl.scrollLeft;
    }
  }

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

  // Save writes the change to the config file only; the running server keeps
  // the old settings until Apply hot-reloads.
  async function save() {
    if (!info || !dirty || saving) return;
    saving = true;
    saveMessage = "";
    saveError = "";
    try {
      await updateModelConfig(model.id, editedYaml);
      info = { ...info, yaml: editedYaml };
      saveMessage =
        "Saved. The change is on disk but not active yet — press Apply to hot-reload.";
    } catch (err) {
      saveError = String(err);
    } finally {
      saving = false;
    }
  }

  // Apply hot-reloads the saved config. It stops running models, so the UI
  // says so before the click.
  async function applyChanges() {
    if (applying || !$configDirty) return;
    applying = true;
    applyMessage = "";
    try {
      await triggerReload();
      applyMessage = "Applying saved settings — running models were stopped and will start on demand.";
    } catch (err) {
      applyMessage = `Apply failed: ${err}`;
    } finally {
      applying = false;
    }
  }

  async function keepCapabilities() {
    if (pendingBusy) return;
    accepting = true;
    pendingMessage = "";
    pendingError = "";
    try {
      await acceptModelCapabilities(model.id);
      pendingMessage = "Kept. The auto-filled values are now plain config.";
      await refreshAfterCapabilityDecision();
    } catch (err) {
      pendingError = String(err);
    } finally {
      accepting = false;
    }
  }

  async function discardCapabilities() {
    if (pendingBusy) return;
    discarding = true;
    pendingMessage = "";
    pendingError = "";
    try {
      await discardModelCapabilities(model.id);
      pendingMessage = "Discarded. The auto-filled values were removed.";
      await refreshAfterCapabilityDecision();
    } catch (err) {
      pendingError = String(err);
    } finally {
      discarding = false;
    }
  }

  async function refreshAfterCapabilityDecision() {
    const id = model.id;
    try {
      const fresh = await fetchModelConfig(id);
      if (model.id === id) {
        editedYaml = fresh.yaml;
        if (info) info = { ...info, yaml: fresh.yaml };
      }
    } catch {
      /* the model list update below still clears the badge */
    }
    void fetchPlaygroundModels();
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
        "Saved. Apply (or the next config reload) activates it everywhere.";
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

  {#if !model.peerID && (model.config_update_pending?.length ?? 0) > 0}
    <Card.Root class="shrink-0 gap-0 overflow-hidden py-0">
      <Card.Header class="flex flex-row items-center gap-3 border-b px-4 py-2">
        <Card.Title class="text-sm font-semibold text-warning">Capabilities update pending</Card.Title>
        <span class="text-muted-foreground text-xs">
          llama-swap auto-filled: {model.config_update_pending?.join(", ")}
        </span>
        <div class="ml-auto flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onclick={keepCapabilities}
            disabled={pendingBusy}
          >
            {accepting ? "Keeping…" : "Keep"}
          </Button>
          <Button
            variant="outline"
            size="sm"
            onclick={discardCapabilities}
            disabled={pendingBusy}
          >
            {discarding ? "Discarding…" : "Discard"}
          </Button>
        </div>
      </Card.Header>

      {#if pendingMessage}
        <div class="border-b bg-muted/40 px-4 py-2 text-xs text-green-600 dark:text-green-400">
          {pendingMessage}
        </div>
      {:else if pendingError}
        <div class="border-b bg-destructive/10 px-4 py-2 text-xs text-destructive">
          {pendingError}
        </div>
      {/if}

      <Card.Content class="p-3">
        <p class="text-muted-foreground text-xs">
          These fields were discovered from the model and written into the
          config under a pending marker (highlighted yellow in the editor).
          Keep to make them permanent config; Discard to remove them — the
          values stop being advertised and are not re-added on the next probe.
        </p>
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
        <Button size="sm" onclick={save} disabled={!dirty || saving || readOnly}>
          {saving ? "Saving…" : "Save"}
        </Button>
        <Button size="sm" onclick={applyChanges} disabled={applying || !$configDirty}>
          {applying ? "Applying…" : "Apply"}
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

    {#if applyMessage}
      <div class="border-b bg-muted/40 px-4 py-2 text-xs text-green-600 dark:text-green-400">
        {applyMessage}
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
            block are replaced; comments elsewhere are preserved. Save writes
            the file; Apply hot-reloads it.
            {#if hasMarkerLine}
              Yellow lines are auto-filled by llama-swap (see the pending
              banner).
            {/if}
          </p>
        {/if}
        {#if hasMarkerLine}
          <!--
            The highlighted editor is a bare <textarea>, not the shadcn
            Textarea: its base classes (text-base, md:text-sm) would fight
            the backdrop's, and the two text layers must share byte-identical
            metrics. Soft-wrap (no wrap="off") so long lines wrap in both
            layers at the same points.
          -->
          <div class="relative">
            <pre
              bind:this={backdropEl}
              class="pointer-events-none absolute inset-0 m-0 select-none overflow-hidden whitespace-pre-wrap rounded-lg border border-input px-2.5 py-2 font-mono text-xs leading-5"
              aria-hidden="true"
            >
              {#each backdropLines as line, i (i)}
                <span class={markerLineFlags[i] ? "bg-warning/25" : ""}>{line}</span>
              {/each}
            </pre>
            <textarea
              bind:this={editorEl}
              class="relative z-10 block w-full min-h-[320px] resize-none whitespace-pre-wrap rounded-lg border border-input bg-transparent px-2.5 py-2 font-mono text-xs leading-5 outline-none"
              bind:value={editedYaml}
              readonly={readOnly}
              spellcheck="false"
              onscroll={syncBackdropScroll}
            ></textarea>
          </div>
        {:else}
          <Textarea
            class="min-h-[320px] font-mono text-xs"
            bind:value={editedYaml}
            readonly={readOnly}
            spellcheck="false"
          />
        {/if}
      {/if}
    </Card.Content>
  </Card.Root>
</div>
