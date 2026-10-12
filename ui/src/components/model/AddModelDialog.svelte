<script lang="ts">
  import {
    addModelConfig,
    fetchBackends,
    type BackendInfo,
  } from "../../lib/modelConfigApi";
  import { isValidModelID } from "../../lib/modelId";
  import * as Dialog from "$lib/components/ui/dialog/index.js";
  import { Button } from "$lib/components/ui/button/index.js";
  import { Input } from "$lib/components/ui/input/index.js";
  import * as Label from "$lib/components/ui/label/index.js";
  import * as Select from "$lib/components/ui/select/index.js";

  interface Props {
    open: boolean;
    onclose: () => void;
    onsaved: () => void;
  }

  let { open, onclose, onsaved }: Props = $props();

  interface ExtraFile {
    flag: string;
    path: string;
  }

  const extraFlags = [
    { value: "--mmproj", label: "mmproj (vision)" },
    { value: "--model-draft", label: "model draft" },
    { value: "--spec-draft-model", label: "spec draft model" },
  ];

  let modelId = $state("");
  let backendPath = $state("");
  let backends = $state<BackendInfo[]>([]);
  let mainFile = $state("");
  let extras = $state<ExtraFile[]>([]);
  let ctxSize = $state(32768);
  let ttl = $state(600);
  let saving = $state(false);
  let error = $state("");

  let idValid = $derived(isValidModelID(modelId));
  let canSave = $derived(
    idValid && backendPath.trim() !== "" && mainFile.trim() !== "" && !saving,
  );

  $effect(() => {
    if (open) {
      fetchBackends()
        .then((list) => (backends = list))
        .catch(() => (backends = []));
    }
  });

  function reset() {
    modelId = "";
    mainFile = "";
    extras = [];
    ctxSize = 32768;
    ttl = 600;
    saving = false;
    error = "";
  }

  function close() {
    reset();
    onclose();
  }

  function buildYaml(): string {
    const files = [`-m "${mainFile.trim().replace(/\\/g, "/")}"`];
    for (const extra of extras) {
      if (!extra.path.trim()) continue;
      files.push(`${extra.flag} "${extra.path.trim().replace(/\\/g, "/")}"`);
    }
    return [
      "capabilities:",
      `  context: ${ctxSize}`,
      "metadata:",
      "  llamaswap:",
      `    effective_context_per_slot: "${ctxSize}"`,
      "cmd: |",
      `      ${backendPath.trim()} --jinja --port \${PORT} ${files.join(" ")} --fit on --fit-ctx 65536 --fit-target 1024 -fa on -ngl 99 -ctk q4_0 -ctv q4_0`,
      `ttl: ${ttl}`,
      "",
    ].join("\n");
  }

  async function save() {
    if (!canSave) return;
    saving = true;
    error = "";
    try {
      await addModelConfig(modelId.trim(), buildYaml());
      onsaved();
      close();
    } catch (err) {
      error = String(err);
    } finally {
      saving = false;
    }
  }
</script>

<Dialog.Root
  {open}
  onOpenChange={(v) => {
    if (!v) close();
  }}
>
  <Dialog.Content class="max-h-[90vh] overflow-auto sm:max-w-lg">
    <Dialog.Header>
      <Dialog.Title>Add model</Dialog.Title>
      <Dialog.Description>
        The model is saved to the config file; press Apply changes in the
        header to start serving it (running models are stopped). New models are
        served by the main endpoint. Custom listeners are never modified
        automatically.
      </Dialog.Description>
    </Dialog.Header>

    <div class="grid gap-4 py-2">
      <div class="grid gap-1.5">
        <Label.Root for="add-model-id">Model ID</Label.Root>
        <Input id="add-model-id" placeholder="my-model-c32768" bind:value={modelId} />
        {#if modelId && !idValid}
          <p class="text-destructive text-xs">
            Use letters, digits, '-', '_', '.', ':' — must start alphanumeric.
          </p>
        {/if}
      </div>

      <div class="grid gap-1.5">
        <Label.Root for="add-model-backend">Backend</Label.Root>
        {#if backends.length > 0}
          <Select.Root
            type="single"
            value={backendPath}
            onValueChange={(v) => v && (backendPath = v)}
          >
            <Select.Trigger id="add-model-backend" class="w-full font-mono text-xs">
              {backendPath || "Select a backend…"}
            </Select.Trigger>
            <Select.Content>
              {#each backends as b (b.path)}
                <Select.Item value={b.path}>
                  {b.path}
                  <span class="text-muted-foreground">({b.models.length} model{b.models.length === 1 ? "" : "s"})</span>
                </Select.Item>
              {/each}
            </Select.Content>
          </Select.Root>
        {:else}
          <Input
            id="add-model-backend"
            class="font-mono text-xs"
            placeholder="/opt/llama.cpp/llama-server"
            bind:value={backendPath}
          />
        {/if}
      </div>

      <div class="grid gap-1.5">
        <Label.Root for="add-model-file">Model file (gguf)</Label.Root>
        <Input
          id="add-model-file"
          class="font-mono text-xs"
          placeholder={"E:/LLM Models/org/model-Q4_K_M.gguf"}
          bind:value={mainFile}
        />
      </div>

      {#each extras as extra, i (i)}
        <div class="flex items-end gap-2">
          <div class="grid w-44 gap-1.5">
            <Label.Root for={`extra-flag-${i}`}>Extra file type</Label.Root>
            <Select.Root
              type="single"
              value={extra.flag}
              onValueChange={(v) => v && (extras[i].flag = v)}
            >
              <Select.Trigger id={`extra-flag-${i}`} class="w-full text-xs">
                {extraFlags.find((f) => f.value === extra.flag)?.label ?? extra.flag}
              </Select.Trigger>
              <Select.Content>
                {#each extraFlags as flag (flag.value)}
                  <Select.Item value={flag.value}>{flag.label}</Select.Item>
                {/each}
              </Select.Content>
            </Select.Root>
          </div>
          <div class="grid flex-1 gap-1.5">
            <Label.Root for={`extra-path-${i}`}>Path</Label.Root>
            <Input
              id={`extra-path-${i}`}
              class="font-mono text-xs"
              bind:value={extra.path}
            />
          </div>
          <Button
            variant="ghost"
            size="sm"
            class="text-destructive"
            onclick={() => (extras = extras.filter((_, j) => j !== i))}
          >
            Remove
          </Button>
        </div>
      {/each}

      {#if extras.length < 2}
        <Button
          variant="outline"
          size="sm"
          class="w-fit"
          onclick={() => extras = [...extras, { flag: "--mmproj", path: "" }]}
        >
          + Add file (mmproj / draft)
        </Button>
      {/if}

      <div class="grid grid-cols-2 gap-4">
        <div class="grid gap-1.5">
          <Label.Root for="add-model-ctx">Context size</Label.Root>
          <Input id="add-model-ctx" type="number" bind:value={ctxSize} />
        </div>
        <div class="grid gap-1.5">
          <Label.Root for="add-model-ttl">TTL (seconds)</Label.Root>
          <Input id="add-model-ttl" type="number" bind:value={ttl} />
        </div>
      </div>

      {#if error}
        <p class="text-destructive text-sm">{error}</p>
      {/if}
    </div>

    <Dialog.Footer class="gap-2 sm:justify-end">
      <Button variant="outline" onclick={close}>Cancel</Button>
      <Button onclick={save} disabled={!canSave}>
        {saving ? "Adding…" : "Add model"}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
