<script lang="ts">
  import DOMPurify from "dompurify";
  import { hubRepoDetail } from "../stores/api";
  import { renderMarkdown } from "../lib/markdown";
  import type { HubRepoDetail } from "../lib/types";

  interface Props {
    repo: string | null; // repo id to show, null = closed
    onclose: () => void;
  }

  let { repo, onclose }: Props = $props();

  let dialogEl: HTMLDialogElement | undefined = $state();
  let detail = $state<HubRepoDetail | null>(null);
  let loading = $state(false);
  let errorMessage = $state("");

  $effect(() => {
    if (repo && dialogEl) {
      dialogEl.showModal();
      loadDetail(repo);
    } else if (!repo && dialogEl) {
      dialogEl.close();
    }
  });

  async function loadDetail(repoId: string): Promise<void> {
    detail = null;
    errorMessage = "";
    loading = true;
    try {
      detail = await hubRepoDetail(repoId);
    } catch (e) {
      errorMessage = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  // READMEs are third-party content (any HF repo author) and the markdown
  // pipeline allows raw HTML, so sanitize before {@html} insertion. DOMPurify
  // strips scripts, event handlers and javascript:/data: URLs while keeping
  // the class attributes hljs/KaTeX styling needs.
  let readmeHtml = $derived(
    detail?.readme
      ? DOMPurify.sanitize(renderMarkdown(detail.readme), {
          USE_PROFILES: { html: true },
          FORBID_TAGS: ["style", "iframe", "form"],
          FORBID_ATTR: ["style"],
        })
      : ""
  );

  function formatDate(iso: string): string {
    if (!iso) return "";
    const d = new Date(iso);
    return isNaN(d.getTime()) ? iso : d.toLocaleDateString();
  }
</script>

<dialog
  bind:this={dialogEl}
  onclose={() => onclose()}
  class="bg-surface text-txtmain rounded-lg shadow-xl max-w-4xl w-full max-h-[90vh] p-0 backdrop:bg-black/50 m-auto"
>
  <div class="flex flex-col max-h-[90vh]">
    <div class="flex justify-between items-center p-4 border-b border-card-border">
      <h2 class="text-xl font-bold pb-0">
        {repo}
        {#if repo}
          <a
            href="https://huggingface.co/{repo}"
            target="_blank"
            rel="noopener noreferrer"
            class="text-sm font-normal text-txtsecondary underline ml-2">open on HF ↗</a
          >
        {/if}
      </h2>
      <button onclick={() => dialogEl?.close()} class="text-txtsecondary hover:text-txtmain text-2xl leading-none">
        &times;
      </button>
    </div>

    <div class="overflow-y-auto flex-1 p-4 space-y-4">
      {#if loading}
        <p class="text-txtsecondary">Loading…</p>
      {:else if errorMessage}
        <p class="text-red-500">{errorMessage}</p>
      {:else if detail}
        <div class="flex flex-wrap gap-x-6 gap-y-1 text-sm text-txtsecondary">
          {#if detail.pipelineTag}<span>Task: <span class="text-txtmain">{detail.pipelineTag}</span></span>{/if}
          <span>Downloads: <span class="text-txtmain">{detail.downloads.toLocaleString()}</span></span>
          <span>Likes: <span class="text-txtmain">{detail.likes.toLocaleString()}</span></span>
          {#if detail.lastModified}<span>Updated: <span class="text-txtmain">{formatDate(detail.lastModified)}</span></span>{/if}
        </div>

        {#if detail.tags.length > 0}
          <div class="flex flex-wrap gap-1">
            {#each detail.tags as tag (tag)}
              <span class="text-xs px-2 py-0.5 rounded-full border border-gray-200 dark:border-white/10 text-txtsecondary">{tag}</span>
            {/each}
          </div>
        {/if}

        {#if readmeHtml}
          <div class="border-t border-card-border pt-4 prose prose-sm dark:prose-invert max-w-none overflow-x-auto">
            {@html readmeHtml}
          </div>
        {:else}
          <p class="text-txtsecondary italic">This repo has no model card.</p>
        {/if}
      {/if}
    </div>
  </div>
</dialog>
