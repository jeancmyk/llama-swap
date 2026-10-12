<script lang="ts">
  import { push } from "svelte-spa-router";
  import { addModelConfig } from "../lib/modelConfigApi";
  import { isValidModelID } from "../lib/modelId";
  import * as Card from "$lib/components/ui/card/index.js";
  import { Button } from "$lib/components/ui/button/index.js";
  import { Input } from "$lib/components/ui/input/index.js";
  import * as Label from "$lib/components/ui/label/index.js";
  import { ArrowLeft, Loader2 } from "@lucide/svelte";

  // Starting point for a new model: the smallest useful cmd plus the most
  // common optional keys as comments. Nothing here is backend-specific once
  // the user edits the cmd; any command that serves OpenAI-style HTTP on
  // ${PORT} works. Comments are written into the config file and preserved
  // by later edits, so they can teach in place.
  const TEMPLATE = [
    "# cmd is required: any command that serves OpenAI-style HTTP on ${PORT}.",
    "cmd: llama-server --jinja --port ${PORT} -m /path/to/model.gguf",
    "# Optional — uncomment and adjust:",
    "# ttl: 600",
    "# capabilities:",
    "#   context: 8192",
    "# proxy: http://127.0.0.1:8080  # attach to an already-running backend",
    "",
  ].join("\n");

  let modelId = $state("");
  let yaml = $state(TEMPLATE);
  let saving = $state(false);
  let error = $state("");
  let savedId = $state("");

  let idTrimmed = $derived(modelId.trim());
  let idValid = $derived(isValidModelID(idTrimmed));
  let canSave = $derived(idValid && !saving && yaml.trim().length > 0);

  async function save(): Promise<void> {
    if (!canSave) return;
    saving = true;
    error = "";
    try {
      await addModelConfig(idTrimmed, yaml);
      savedId = idTrimmed;
    } catch (err) {
      error = String(err);
    } finally {
      saving = false;
    }
  }
</script>

<Card.Root class="mx-auto w-full max-w-3xl gap-4 p-6">
  <div class="flex items-center gap-2">
    <Button variant="ghost" size="sm" onclick={() => push("/models")}>
      <ArrowLeft class="size-4" />
      Models
    </Button>
    <Card.Title class="text-lg">New model</Card.Title>
  </div>

  <p class="text-muted-foreground text-sm">
    Pick an ID and edit the block below. Save appends it to the config file;
    press Apply changes on the Models page to start serving it.
  </p>

  <div class="grid gap-1.5">
    <Label.Root for="new-model-id">Model ID</Label.Root>
    <Input
      id="new-model-id"
      class="font-mono text-xs"
      maxlength={128}
      placeholder="my-model"
      disabled={savedId !== ""}
      bind:value={modelId}
    />
    {#if idTrimmed && !idValid}
      <p class="text-destructive text-xs">
        Use letters, digits, '-', '_', '.', ':' — must start alphanumeric.
      </p>
    {/if}
  </div>

  <div class="grid gap-1.5">
    <Label.Root for="new-model-yaml">Model settings (YAML)</Label.Root>
    <textarea
      id="new-model-yaml"
      class="block min-h-[320px] w-full resize-y rounded-lg border border-input bg-transparent px-2.5 py-2 font-mono text-xs leading-5 outline-none"
      spellcheck="false"
      disabled={savedId !== ""}
      bind:value={yaml}
    ></textarea>
  </div>

  {#if error}
    <p class="bg-destructive/15 text-destructive rounded-md p-2 text-xs break-words">{error}</p>
  {/if}
  {#if savedId}
    <p class="bg-green-500/15 text-green-700 dark:text-green-400 rounded-md p-2 text-xs">
      “{savedId}” was appended to the config file. Apply changes on the Models
      page to start serving it, or edit it further from its Config tab.
    </p>
  {/if}

  <div class="flex items-center gap-2">
    <Button onclick={() => void save()} disabled={!canSave}>
      {#if saving}
        <Loader2 class="size-4 animate-spin" />
      {/if}
      {saving ? "Saving…" : "Save"}
    </Button>
    {#if savedId}
      <Button variant="outline" onclick={() => push("/models")}>
        Go to Models to Apply
      </Button>
    {/if}
  </div>
</Card.Root>
