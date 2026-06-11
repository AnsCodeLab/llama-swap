<script lang="ts">
  import { hubPopular, hubSearch, hubRepoFiles, hubDownload, downloads, HubDisabledError } from "../stores/api";
  import type { HubRepo, HubFile } from "../lib/types";

  let query = $state("");
  let repos = $state<HubRepo[]>([]);
  let loading = $state(false);
  let disabledMessage = $state("");
  let errorMessage = $state("");

  let expandedRepo = $state<string | null>(null);
  let repoFiles = $state<HubFile[]>([]);
  let filesLoading = $state(false);

  // files already being downloaded (keyed by repo/file)
  let activeFiles = $derived(new Set($downloads.filter((d) => d.state === "downloading").map((d) => `${d.repo}/${d.file}`)));
  let completedFiles = $derived(new Set($downloads.filter((d) => d.state === "completed").map((d) => `${d.repo}/${d.file}`)));

  async function loadRepos(): Promise<void> {
    loading = true;
    errorMessage = "";
    try {
      repos = query.trim() ? await hubSearch(query.trim()) : await hubPopular();
    } catch (e) {
      if (e instanceof HubDisabledError) {
        disabledMessage = e.message;
      } else {
        errorMessage = e instanceof Error ? e.message : String(e);
      }
    } finally {
      loading = false;
    }
  }

  async function toggleRepo(repoId: string): Promise<void> {
    if (expandedRepo === repoId) {
      expandedRepo = null;
      return;
    }
    expandedRepo = repoId;
    repoFiles = [];
    filesLoading = true;
    errorMessage = "";
    try {
      repoFiles = await hubRepoFiles(repoId);
    } catch (e) {
      errorMessage = e instanceof Error ? e.message : String(e);
    } finally {
      filesLoading = false;
    }
  }

  async function startDownload(repoId: string, file: HubFile): Promise<void> {
    errorMessage = "";
    try {
      await hubDownload(repoId, file.name);
    } catch (e) {
      errorMessage = e instanceof Error ? e.message : String(e);
    }
  }

  function formatBytes(n: number): string {
    if (n >= 1024 ** 3) return `${(n / 1024 ** 3).toFixed(1)} GB`;
    if (n >= 1024 ** 2) return `${(n / 1024 ** 2).toFixed(1)} MB`;
    return `${(n / 1024).toFixed(0)} KB`;
  }

  $effect(() => {
    loadRepos();
  });
</script>

<div class="card h-full flex flex-col">
  <div class="shrink-0">
    <h2>Discover</h2>
    {#if disabledMessage}
      <p class="text-txtsecondary mt-2">
        Hub downloads are disabled. Set <code>modelsDir</code> in your configuration file to enable downloading
        models from HuggingFace.
      </p>
    {:else}
      <form
        class="flex gap-2 mt-2"
        onsubmit={(e) => {
          e.preventDefault();
          loadRepos();
        }}
      >
        <input
          type="text"
          class="flex-1 px-3 py-1 rounded border border-gray-200 dark:border-white/10 bg-surface"
          placeholder="Search GGUF models on HuggingFace…"
          bind:value={query}
        />
        <button class="btn text-base" type="submit" disabled={loading}>{loading ? "Searching…" : "Search"}</button>
      </form>
    {/if}
    {#if errorMessage}
      <p class="text-red-500 mt-2">{errorMessage}</p>
    {/if}
  </div>

  {#if !disabledMessage}
    <div class="flex-1 overflow-y-auto mt-2">
      <table class="w-full">
        <thead class="sticky top-0 bg-card z-10">
          <tr class="text-left border-b border-gray-200 dark:border-white/10 bg-surface">
            <th>Repository</th>
            <th class="w-24 text-right">Downloads</th>
            <th class="w-16 text-right">Likes</th>
          </tr>
        </thead>
        <tbody>
          {#each repos as repo (repo.id)}
            <tr class="border-b hover:bg-secondary-hover border-gray-200 cursor-pointer" onclick={() => toggleRepo(repo.id)}>
              <td class="font-semibold">{repo.id}</td>
              <td class="text-right">{repo.downloads.toLocaleString()}</td>
              <td class="text-right">{repo.likes.toLocaleString()}</td>
            </tr>
            {#if expandedRepo === repo.id}
              <tr class="border-b border-gray-200">
                <td colspan="3" class="pl-8 py-2">
                  {#if filesLoading}
                    <p class="text-txtsecondary">Loading files…</p>
                  {:else if repoFiles.length === 0}
                    <p class="text-txtsecondary">No GGUF files found in this repo.</p>
                  {:else}
                    <table class="w-full">
                      <tbody>
                        {#each repoFiles as file (file.name)}
                          <tr>
                            <td>{file.name}</td>
                            <td class="w-20">{file.quant}</td>
                            <td class="w-24 text-right">{formatBytes(file.size)}</td>
                            <td class="w-32 text-right">
                              {#if file.downloaded || completedFiles.has(`${repo.id}/${file.name}`)}
                                <span class="status status--ready">Downloaded ✓</span>
                              {:else if activeFiles.has(`${repo.id}/${file.name}`)}
                                <span class="status status--starting">downloading</span>
                              {:else}
                                <button class="btn btn--sm" onclick={() => startDownload(repo.id, file)}>Download</button>
                              {/if}
                            </td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  {/if}
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>
