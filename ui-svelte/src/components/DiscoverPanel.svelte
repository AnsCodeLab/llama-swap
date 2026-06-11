<script lang="ts">
  import { hubPopular, hubSearch, hubRepoFiles, hubDownload, hubHardware, downloads, HubDisabledError } from "../stores/api";
  import RepoDetailDialog from "./RepoDetailDialog.svelte";
  import { rateModelFit, typeHint, type Hardware } from "../lib/modelFit";
  import type { HubRepo, HubFile } from "../lib/types";

  let query = $state("");
  let repos = $state<HubRepo[]>([]);
  let loading = $state(false);
  let disabledMessage = $state("");
  let errorMessage = $state("");

  let expandedRepo = $state<string | null>(null);
  let repoFiles = $state<HubFile[]>([]);
  let filesLoading = $state(false);
  let detailRepo = $state<string | null>(null);
  let hardware = $state<Hardware | null>(null);

  type SortKey = "downloads" | "likes" | "lastModified" | "id";
  let sortKey = $state<SortKey>("downloads");

  let sortedRepos = $derived.by(() => {
    const arr = [...repos];
    arr.sort((a, b) => {
      switch (sortKey) {
        case "id":
          return a.id.localeCompare(b.id);
        case "lastModified":
          return (b.lastModified || "").localeCompare(a.lastModified || "");
        default:
          return b[sortKey] - a[sortKey];
      }
    });
    return arr;
  });

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

  function formatCount(n: number): string {
    if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
    if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
    return `${n}`;
  }

  function timeAgo(iso: string): string {
    if (!iso) return "";
    const then = new Date(iso).getTime();
    if (isNaN(then)) return "";
    const days = Math.floor((Date.now() - then) / 86_400_000);
    if (days < 1) return "today";
    if (days < 31) return `${days} day${days === 1 ? "" : "s"} ago`;
    if (days < 365) {
      const months = Math.floor(days / 30);
      return `${months} month${months === 1 ? "" : "s"} ago`;
    }
    const years = Math.floor(days / 365);
    return `${years} year${years === 1 ? "" : "s"} ago`;
  }

  $effect(() => {
    loadRepos();
    hubHardware()
      .then((hw) => (hardware = hw))
      .catch(() => (hardware = null)); // no badges when unavailable
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
      <div class="flex justify-end items-center gap-2 mt-2 text-sm text-txtsecondary">
        <label for="hub-sort">Sort:</label>
        <select id="hub-sort" class="bg-surface border border-gray-200 dark:border-white/10 rounded px-2 py-1" bind:value={sortKey}>
          <option value="downloads">Most downloads</option>
          <option value="likes">Most likes</option>
          <option value="lastModified">Recently updated</option>
          <option value="id">Name (A–Z)</option>
        </select>
      </div>
    {/if}
    {#if errorMessage}
      <p class="text-red-500 mt-2">{errorMessage}</p>
    {/if}
  </div>

  {#if !disabledMessage}
    <div class="flex-1 overflow-y-auto mt-2 space-y-2">
      {#each sortedRepos as repo (repo.id)}
        <div class="border border-gray-200 dark:border-white/10 rounded-lg hover:bg-secondary-hover">
          <button class="w-full text-left px-4 py-3 cursor-pointer" onclick={() => toggleRepo(repo.id)}>
            <div class="flex justify-between items-start gap-2">
              <span class="font-semibold break-all">{repo.id}</span>
              <span
                class="shrink-0 text-txtsecondary hover:text-txtmain"
                title="Model details"
                role="button"
                tabindex="0"
                onclick={(e) => {
                  e.stopPropagation();
                  detailRepo = repo.id;
                }}
                onkeydown={(e) => {
                  if (e.key === "Enter" || e.key === " ") {
                    e.stopPropagation();
                    detailRepo = repo.id;
                  }
                }}
              >
                <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-5 h-5">
                  <path fill-rule="evenodd" d="M2.25 12c0-5.385 4.365-9.75 9.75-9.75s9.75 4.365 9.75 9.75-4.365 9.75-9.75 9.75S2.25 17.385 2.25 12Zm8.706-1.442c1.146-.573 2.437.463 2.126 1.706l-.709 2.836.042-.02a.75.75 0 0 1 .67 1.34l-.04.022c-1.147.573-2.438-.463-2.127-1.706l.71-2.836-.042.02a.75.75 0 1 1-.671-1.34l.041-.022ZM12 9a.75.75 0 1 0 0-1.5.75.75 0 0 0 0 1.5Z" clip-rule="evenodd" />
                </svg>
              </span>
            </div>
            <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-txtsecondary mt-1">
              {#if repo.pipelineTag}
                <span class="px-1.5 py-0.5 rounded border border-gray-200 dark:border-white/10">{repo.pipelineTag}</span>
              {/if}
              {#if typeHint(repo.pipelineTag)}
                <span class="px-1.5 py-0.5 rounded border border-amber-500/50 text-amber-700 dark:text-amber-400">
                  {typeHint(repo.pipelineTag)}
                </span>
              {/if}
              {#if repo.lastModified}
                <span>Updated {timeAgo(repo.lastModified)}</span>
              {/if}
              <span class="flex items-center gap-1" title="{repo.downloads.toLocaleString()} downloads">
                <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-3.5 h-3.5">
                  <path fill-rule="evenodd" d="M12 2.25a.75.75 0 0 1 .75.75v11.69l3.22-3.22a.75.75 0 1 1 1.06 1.06l-4.5 4.5a.75.75 0 0 1-1.06 0l-4.5-4.5a.75.75 0 1 1 1.06-1.06l3.22 3.22V3a.75.75 0 0 1 .75-.75Zm-9 13.5a.75.75 0 0 1 .75.75v2.25a1.5 1.5 0 0 0 1.5 1.5h13.5a1.5 1.5 0 0 0 1.5-1.5V16.5a.75.75 0 0 1 1.5 0v2.25a3 3 0 0 1-3 3H5.25a3 3 0 0 1-3-3V16.5a.75.75 0 0 1 .75-.75Z" clip-rule="evenodd" />
                </svg>
                {formatCount(repo.downloads)}
              </span>
              <span class="flex items-center gap-1" title="{repo.likes.toLocaleString()} likes">
                <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" class="w-3.5 h-3.5">
                  <path d="m11.645 20.91-.007-.003-.022-.012a15.247 15.247 0 0 1-.383-.218 25.18 25.18 0 0 1-4.244-3.17C4.688 15.36 2.25 12.174 2.25 8.25 2.25 5.322 4.714 3 7.688 3A5.5 5.5 0 0 1 12 5.052 5.5 5.5 0 0 1 16.313 3c2.973 0 5.437 2.322 5.437 5.25 0 3.925-2.438 7.111-4.739 9.256a25.175 25.175 0 0 1-4.244 3.17 15.247 15.247 0 0 1-.383.219l-.022.012-.007.004-.003.001a.752.752 0 0 1-.704 0l-.003-.001Z" />
                </svg>
                {formatCount(repo.likes)}
              </span>
            </div>
          </button>

          {#if expandedRepo === repo.id}
            <div class="px-4 pb-3 border-t border-gray-200 dark:border-white/10">
              {#if filesLoading}
                <p class="text-txtsecondary pt-2">Loading files…</p>
              {:else if repoFiles.length === 0}
                <p class="text-txtsecondary pt-2">No GGUF files found in this repo.</p>
              {:else}
                <table class="w-full mt-2">
                  <tbody>
                    {#each repoFiles as file (file.name)}
                      <tr>
                        <td class="break-all">{file.name}</td>
                        <td class="w-20">{file.quant}</td>
                        <td class="w-24 text-right">{formatBytes(file.size)}</td>
                        <td class="w-56 text-right whitespace-nowrap">
                          {@const fit = rateModelFit(file.size, hardware)}
                          {#if fit.level !== "unknown"}
                            <span
                              class="text-xs px-1.5 py-0.5 rounded mr-2
                                {fit.level === 'great' ? 'bg-green-600/20 text-green-600 dark:text-green-400' : ''}
                                {fit.level === 'good' ? 'bg-teal-600/20 text-teal-700 dark:text-teal-400' : ''}
                                {fit.level === 'tight' ? 'bg-amber-500/20 text-amber-700 dark:text-amber-400' : ''}
                                {fit.level === 'too-large' ? 'bg-red-600/20 text-red-600 dark:text-red-400' : ''}"
                              title={fit.reason}>{fit.label}</span>
                          {/if}
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
            </div>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>

<RepoDetailDialog repo={detailRepo} onclose={() => (detailRepo = null)} />
