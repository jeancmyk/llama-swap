<script lang="ts">
  import ListenersPanel from "../components/ListenersPanel.svelte";
</script>

<div class="p-2">
  <div class="mt-4 mb-4">
    <h3 class="text-lg font-semibold">Ports</h3>
    <p class="text-muted-foreground text-sm">
      Additional HTTP listeners that serve a scoped subset of models. The main
      listener always serves every model.
    </p>
  </div>

  <ListenersPanel />
</div>
