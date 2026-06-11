<script lang="ts">
  import { isNarrow } from "../stores/theme";
  import { upstreamLogs } from "../stores/api";
  import ModelsPanel from "../components/ModelsPanel.svelte";
  import DiscoverPanel from "../components/DiscoverPanel.svelte";
  import LogPanel from "../components/LogPanel.svelte";
  import ResizablePanels from "../components/ResizablePanels.svelte";

  let direction = $derived<"horizontal" | "vertical">($isNarrow ? "vertical" : "horizontal");
  let tab = $state<"installed" | "discover">("installed");
</script>

<ResizablePanels {direction} storageKey="models-panel-group">
  {#snippet leftPanel()}
    <div class="h-full flex flex-col">
      <div class="shrink-0 flex gap-2 mb-2">
        <button
          class="btn text-base"
          class:font-bold={tab === "installed"}
          class:underline={tab === "installed"}
          onclick={() => (tab = "installed")}
        >
          Installed
        </button>
        <button
          class="btn text-base"
          class:font-bold={tab === "discover"}
          class:underline={tab === "discover"}
          onclick={() => (tab = "discover")}
        >
          Discover
        </button>
      </div>
      <div class="flex-1 min-h-0">
        {#if tab === "installed"}
          <ModelsPanel />
        {:else}
          <DiscoverPanel />
        {/if}
      </div>
    </div>
  {/snippet}
  {#snippet rightPanel()}
    <LogPanel id="modelsupstream" title="Upstream Logs" logData={$upstreamLogs} />
  {/snippet}
</ResizablePanels>
