<script lang="ts">
  import { onMount } from "svelte";
  import type { Snippet } from "svelte";
  import { link } from "svelte-spa-router";
  import {
    activeProfile,
    configDirty,
    fetchPlaygroundModels,
    models,
    profiles,
    selectorModels,
    unloadAllModels,
  } from "../stores/api";
  import { loadingMessage, loadingPercent, statusDotColor } from "../stores/modelLoad";
  import { showUnlistedModels as showUnlisted, showCapabilityTags } from "../stores/modelDisplay";
  import { listCapabilityBadges, capabilityBadgeClass } from "../lib/capabilities";
  import type { Model } from "../lib/types";
  import ModelLoadButton from "../components/ModelLoadButton.svelte";
  import Tag from "../components/Tag.svelte";
  import AddModelDialog from "../components/model/AddModelDialog.svelte";
  import { removeModelConfig, triggerReload } from "../lib/modelConfigApi";
  import * as Card from "$lib/components/ui/card/index.js";
  import { Button } from "$lib/components/ui/button/index.js";
  import * as Switch from "$lib/components/ui/switch/index.js";
  import * as Label from "$lib/components/ui/label/index.js";
  import * as Dialog from "$lib/components/ui/dialog/index.js";
  import { PowerOff, Loader2, ExternalLink, SquareStack, Plus, Trash2 } from "@lucide/svelte";
  import { modelServerPath } from "../lib/modelUtils";

  let unloadingAll = $state(false);
  let showAddDialog = $state(false);
  let removeTarget = $state<Model | null>(null);
  let removing = $state(false);
  let removeError = $state("");
  let applying = $state(false);
  let applyMessage = $state("");

  onMount(() => {
    void fetchPlaygroundModels();
  });

  async function handleRemoveConfirmed(): Promise<void> {
    if (!removeTarget || removing) return;
    removing = true;
    removeError = "";
    try {
      await removeModelConfig(removeTarget.id);
      removeTarget = null;
      await fetchPlaygroundModels();
    } catch (e) {
      removeError = String(e);
    } finally {
      removing = false;
    }
  }

  let visibleModels = $derived(
    $showUnlisted ? $models : $models.filter((m) => !m.unlisted)
  );
  let localModels = $derived(visibleModels.filter((model) => !model.peerID));
  let peerModels = $derived(visibleModels.filter((model) => model.peerID));
  let selectedProfile = $derived(
    $profiles.find((profile) => profile.id === $activeProfile)
  );
  let profileMappings = $derived(
    Object.entries(selectedProfile?.pins ?? {}).sort(([a], [b]) =>
      a.localeCompare(b, undefined, { numeric: true })
    )
  );

  let readyCount = $derived($models.filter((m) => m.state === "ready").length);
  let anyReady = $derived(readyCount > 0);
  let missingFilesCount = $derived(
    $models.filter((m) => (m.missing_files?.length ?? 0) > 0).length,
  );

  async function handleUnloadAll(): Promise<void> {
    unloadingAll = true;
    try {
      await unloadAllModels();
    } catch (e) {
      console.error(e);
    } finally {
      unloadingAll = false;
    }
  }

  // Apply hot-reloads the saved config. It stops running models, so the
  // button carries that in its message and only appears while the config
  // is dirty.
  async function handleApply(): Promise<void> {
    if (applying) return;
    applying = true;
    applyMessage = "";
    try {
      await triggerReload();
      applyMessage = "Applying saved settings — running models were stopped.";
      // configDirty flips back via the configStatus SSE after the reload
    } catch (e) {
      applyMessage = `Apply failed: ${e}`;
      console.error(e);
    } finally {
      applying = false;
    }
  }
</script>

{#snippet modelRow(model: Model)}
  {@const percent = loadingPercent(model)}
  {@const message = loadingMessage(model)}
  <div class="hover:bg-muted/50 relative flex items-center gap-3 px-4 py-2.5">
    {#if !model.peerID}
      <span class={`size-2.5 shrink-0 rounded-full ${statusDotColor(model)}`}></span>
    {/if}
    <a
      href="/models/{encodeURIComponent(model.id)}"
      use:link
      class="min-w-0 flex-1"
    >
      <div class="truncate text-sm font-medium">{model.name || model.id}</div>
      <div class="text-muted-foreground truncate text-xs">
        {model.id}
        {#if model.aliases && model.aliases.length > 0}
          · {model.aliases.join(", ")}
        {/if}
      </div>
      {#if message}
        <div class="text-muted-foreground truncate text-xs italic" title={message}>{message}</div>
      {/if}
    </a>
    {#if $showCapabilityTags}
      {@const badges = listCapabilityBadges(model)}
      {#if badges.length > 0}
        <div class="hidden min-w-0 flex-wrap items-center gap-1 sm:flex">
          {#each badges as badge (badge.key)}
            <Tag class={`px-1.5 text-[0.625rem] ${capabilityBadgeClass[badge.key] ?? ""}`}>{badge.label}</Tag>
          {/each}
        </div>
      {/if}
    {/if}
    <span class="text-muted-foreground text-xs uppercase tracking-wide">
      {model.state}{#if percent !== undefined}&nbsp;<span class="tabular-nums">{percent}%</span>{/if}
    </span>
    {#if model.unlisted}
      <Tag class="px-1.5 text-[0.625rem] uppercase">unlisted</Tag>
    {/if}
    {#if model.missing_files && model.missing_files.length > 0}
      <span title={model.missing_files.map((f) => `${f.flag} ${f.path}`).join("\n")}>
        <Tag class="bg-warning/15 text-warning px-1.5 text-[0.625rem] uppercase">
          missing files ({model.missing_files.length})
        </Tag>
      </span>
    {/if}
    {#if (model.config_update_pending?.length ?? 0) > 0}
      <span title={`llama-swap auto-filled: ${model.config_update_pending?.join(", ")}`}>
        <Tag class="bg-warning/15 text-warning px-1.5 text-[0.625rem] uppercase">
          update pending
        </Tag>
      </span>
    {/if}
    {#if !model.peerID}
      <a
        href={modelServerPath(model.id)}
        target="_blank"
        rel="noopener noreferrer"
        class="text-muted-foreground hover:text-foreground"
        title="Open model server"
        aria-label="Open model server"
      >
        <ExternalLink class="size-4" />
      </a>
      <ModelLoadButton {model} />
      <button
        type="button"
        class="text-muted-foreground hover:text-destructive"
        title="Remove model from config"
        aria-label="Remove model from config"
        onclick={() => {
          removeError = "";
          removeTarget = model;
        }}
      >
        <Trash2 class="size-4" />
      </button>
    {/if}
    {#if percent !== undefined}
      <!-- Loading progress; the percentage next to the state carries it for screen readers. -->
      <div aria-hidden="true" class="bg-muted absolute inset-x-0 bottom-0 h-0.5">
        <div class="bg-warning h-full transition-[width] duration-500" style:width="{percent}%"></div>
      </div>
    {/if}
  </div>
{/snippet}

{#snippet modelSection(title: string, sectionModels: Model[], headerExtra?: Snippet)}
  <Card.Root class="shrink-0 gap-0 overflow-hidden py-0">
    <Card.Header class="shrink-0 border-b px-4 py-2.5">
      <div class="flex items-center gap-2">
        <Card.Title class="text-sm">{title}</Card.Title>
        <span class="text-muted-foreground text-xs">{sectionModels.length}</span>
        {#if headerExtra}
          <div class="ml-auto flex items-center gap-2">
            {@render headerExtra()}
          </div>
        {/if}
      </div>
    </Card.Header>
    <Card.Content class="p-0">
      {#if sectionModels.length === 0}
        <div class="text-muted-foreground px-4 py-6 text-center text-sm">
          No {title.toLowerCase()} available
        </div>
      {:else}
        <div class="divide-y">
          {#each sectionModels as model (model.id)}
            {@render modelRow(model)}
          {/each}
        </div>
      {/if}
    </Card.Content>
  </Card.Root>
{/snippet}

{#snippet unlistedToggle()}
  <Label.Root for="show-unlisted-toggle" class="text-sm">
    Show unlisted models
  </Label.Root>
  <Switch.Root
    id="show-unlisted-toggle"
    checked={$showUnlisted}
    onCheckedChange={(v) => showUnlisted.set(v)}
  />
  <span class="text-muted-foreground text-xs">
    {$models.filter((m) => m.unlisted).length} unlisted
  </span>
{/snippet}

<div class="flex h-full flex-col gap-4 overflow-y-auto p-2">
  <Card.Root class="shrink-0 gap-0 overflow-hidden py-0">
    <Card.Header class="shrink-0 gap-2 border-b px-4 py-3">
      <div class="flex items-center gap-2">
        <SquareStack class="size-5" />
        <Card.Title class="text-lg">Models</Card.Title>
        <span class="text-muted-foreground text-sm">
          ({visibleModels.length} of {$models.length})
        </span>
        <span class="text-muted-foreground text-xs uppercase tracking-wide">
          {readyCount} ready
        </span>
        {#if missingFilesCount > 0}
          <span class="text-warning text-xs uppercase tracking-wide">
            {missingFilesCount} missing files
          </span>
        {/if}
        {#if $configDirty}
          <span class="text-warning text-xs uppercase tracking-wide" title="Config changes were saved and are waiting for Apply">
            changes saved, not applied
          </span>
        {/if}
        <div class="ml-auto flex items-center gap-2">
          {#if $configDirty}
            <Button
              variant="outline"
              size="sm"
              onclick={handleApply}
              disabled={applying}
              title="Hot-reload the saved config. Running models are stopped."
            >
              {#if applying}
                <Loader2 class="size-3.5 animate-spin" />
              {/if}
              {applying ? "Applying…" : "Apply changes"}
            </Button>
          {/if}
          <Button
            variant="outline"
            size="sm"
            onclick={() => (showAddDialog = true)}
          >
            <Plus class="size-3.5" />
            Add model
          </Button>
          <Button
            variant="outline"
            size="sm"
            onclick={handleUnloadAll}
            disabled={!anyReady || unloadingAll}
          >
            {#if unloadingAll}
              <Loader2 class="size-3.5 animate-spin" />
            {:else}
              <PowerOff class="size-3.5" />
            {/if}
            Unload All
          </Button>
        </div>
      </div>
    </Card.Header>
    {#if applyMessage}
      <div class="border-b bg-muted/40 px-4 py-2 text-xs text-green-600 dark:text-green-400">
        {applyMessage}
      </div>
    {/if}
  </Card.Root>

  <div class="flex min-h-0 shrink-0 flex-col gap-4">
    {#if $profiles.length > 0}
      <Card.Root class="shrink-0 gap-0 overflow-hidden py-0">
        <Card.Header class="shrink-0 border-b px-4 py-2.5">
          <div class="flex items-center gap-2">
            <Card.Title class="text-sm">Profiles</Card.Title>
            {#if selectedProfile}
              <Tag>{$activeProfile}</Tag>
              <Tag class="bg-success/15 text-success">Active</Tag>
            {/if}
            <span class="text-muted-foreground ml-auto text-xs">
              {profileMappings.length} {profileMappings.length === 1 ? "mapping" : "mappings"}
            </span>
          </div>
          {#if selectedProfile?.description}
            <p class="text-muted-foreground text-xs">{selectedProfile.description}</p>
          {/if}
        </Card.Header>
        <Card.Content class="p-0">
          {#if !selectedProfile}
            <div class="text-muted-foreground px-4 py-6 text-center text-sm">
              No active profile
            </div>
          {:else}
            <div class="divide-y">
              {#each profileMappings as [modelID, target] (modelID)}
                {@const targetModel = target
                  ? $models.find((m) => m.id === target) ??
                    $models.find((m) => m.aliases?.includes(target))
                  : undefined}
                <div
                  class="hover:bg-muted/50 flex items-center gap-3 px-4 py-2.5 has-[>button]:py-1.5"
                >
                  {#if targetModel && !targetModel.peerID}
                    <span class={`size-2.5 shrink-0 rounded-full ${statusDotColor(targetModel)}`} role="img" aria-label={`Model ${targetModel.state}`}></span>
                  {/if}
                  <span class="max-w-[45%] truncate text-sm font-medium">{modelID}</span>
                  <span class="text-muted-foreground text-xs" aria-hidden="true">→</span>
                  {#if target}
                    {#if targetModel}
                      <a
                        href="/models/{encodeURIComponent(targetModel.id)}"
                        use:link
                        class="min-w-0 flex-1 truncate text-sm hover:text-foreground hover:underline"
                      >{target}</a>
                      {#if !targetModel.peerID}
                        <ModelLoadButton model={targetModel} />
                      {/if}
                    {:else}
                      <span class="min-w-0 truncate text-sm">{target}</span>
                    {/if}
                  {:else}
                    <Tag class="px-1.5 text-[0.625rem] uppercase">disabled</Tag>
                  {/if}
                </div>
              {/each}
            </div>
          {/if}
        </Card.Content>
      </Card.Root>
    {/if}

    {#if $selectorModels.length > 0}
      <Card.Root class="shrink-0 gap-0 overflow-hidden py-0">
        <Card.Header class="shrink-0 border-b px-4 py-2.5">
          <div class="flex items-center gap-2">
            <Card.Title class="text-sm">Selectors</Card.Title>
            <span class="text-muted-foreground ml-auto text-xs">
              {$selectorModels.length} {$selectorModels.length === 1 ? "selector" : "selectors"}
            </span>
          </div>
        </Card.Header>
        <Card.Content class="p-0">
          <div class="divide-y">
            {#each $selectorModels as selector (selector.id)}
              <div class="hover:bg-muted/50 flex items-center gap-3 px-4 py-2.5">
                <div class="min-w-0 flex-1">
                  <div class="truncate text-sm font-medium">
                    {selector.name ? `${selector.id} - ${selector.name}` : selector.id}
                  </div>
                  {#if selector.description}
                    <div class="text-muted-foreground truncate text-xs">
                      {selector.description}
                    </div>
                  {/if}
                  <div class="text-muted-foreground flex flex-wrap items-center gap-x-1 text-xs">
                    <span>targets:</span>
                    {#each selector.targets ?? [] as target, i (target)}
                      {#if i > 0}<span>,</span>{/if}
                      <a
                        href="/models/{encodeURIComponent(target)}"
                        use:link
                        class="hover:text-foreground hover:underline"
                      >{target}</a>
                    {/each}
                  </div>
                </div>
                {#if selector.strategy === "spillover" && selector.spillover}
                  <Tag>spillover {selector.spillover}</Tag>
                {/if}
                <Tag class="px-1.5 text-[0.625rem] uppercase">{selector.strategy}</Tag>
              </div>
            {/each}
          </div>
        </Card.Content>
      </Card.Root>
    {/if}

    {@render modelSection("Local models", localModels, unlistedToggle)}
    {@render modelSection("Peer models", peerModels)}
  </div>
</div>

<AddModelDialog
  open={showAddDialog}
  onclose={() => (showAddDialog = false)}
  onsaved={() => void fetchPlaygroundModels()}
/>

<Dialog.Root
  open={removeTarget !== null}
  onOpenChange={(v) => {
    if (!v) removeTarget = null;
  }}
>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>Remove model</Dialog.Title>
      <Dialog.Description>
        Remove <span class="font-mono">{removeTarget?.id}</span> from the
        configuration? Its entry is deleted from the config file and exact
        references are dropped from custom listeners. This cannot be undone.
      </Dialog.Description>
    </Dialog.Header>
    {#if removeError}
      <p class="text-destructive text-sm">{removeError}</p>
    {/if}
    <Dialog.Footer class="gap-2 sm:justify-end">
      <Button variant="outline" onclick={() => (removeTarget = null)}>
        Cancel
      </Button>
      <Button variant="destructive" onclick={handleRemoveConfirmed} disabled={removing}>
        {removing ? "Removing…" : "Remove"}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
