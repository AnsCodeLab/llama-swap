<script lang="ts">
  import { onMount } from "svelte";
  import { getAuthStatus, setAuthCredentials, listApiKeys, generateApiKey, deleteApiKey, revealApiKey } from "../stores/api";
  import type { AuthStatus, ApiKeyEntry } from "../lib/types";

  let authStatus = $state<AuthStatus>({ enabled: false, username: "" });
  let username = $state("");
  let password = $state("");
  let confirmPassword = $state("");
  let authError = $state("");
  let authSaving = $state(false);

  let apiKeys = $state<ApiKeyEntry[]>([]);
  let newKeyLabel = $state("");
  let generatedKey = $state("");
  let generatedKeyCopied = $state(false);
  let apiKeysError = $state("");
  let copiedRowId = $state("");

  async function loadAuthStatus(): Promise<void> {
    authStatus = await getAuthStatus();
    username = authStatus.username;
  }

  async function loadApiKeys(): Promise<void> {
    apiKeys = await listApiKeys();
  }

  onMount(() => {
    loadAuthStatus();
    loadApiKeys();
  });

  async function handleSaveAuth(): Promise<void> {
    authError = "";
    if (password !== confirmPassword) {
      authError = "Passwords do not match";
      return;
    }
    if (username === "" && password === "") {
      if (!confirm("Anyone will be able to access this server. Continue?")) {
        return;
      }
    }
    authSaving = true;
    try {
      await setAuthCredentials(username, password);
      password = "";
      confirmPassword = "";
      await loadAuthStatus();
    } catch (e) {
      authError = e instanceof Error ? e.message : String(e);
    } finally {
      authSaving = false;
    }
  }

  async function handleGenerateKey(): Promise<void> {
    apiKeysError = "";
    try {
      const result = await generateApiKey(newKeyLabel.trim());
      generatedKey = result.key;
      generatedKeyCopied = false;
      newKeyLabel = "";
      await loadApiKeys();
    } catch (e) {
      apiKeysError = e instanceof Error ? e.message : String(e);
    }
  }

  async function handleDeleteKey(entry: ApiKeyEntry): Promise<void> {
    const label = entry.label || entry.maskedKey;
    if (!confirm(`Delete API key "${label}"? Any client using it will lose access immediately.`)) {
      return;
    }
    apiKeysError = "";
    try {
      await deleteApiKey(entry.id);
      await loadApiKeys();
    } catch (e) {
      apiKeysError = e instanceof Error ? e.message : String(e);
    }
  }

  async function copyTextToClipboard(text: string): Promise<void> {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return;
    }
    // Fallback for non-secure contexts (HTTP on a non-localhost origin),
    // where navigator.clipboard is unavailable.
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.style.position = "fixed";
    textarea.style.left = "-9999px";
    document.body.appendChild(textarea);
    textarea.select();
    document.execCommand("copy");
    document.body.removeChild(textarea);
  }

  async function copyGeneratedKey(): Promise<void> {
    try {
      await copyTextToClipboard(generatedKey);
      generatedKeyCopied = true;
      setTimeout(() => (generatedKeyCopied = false), 1500);
    } catch (err) {
      console.error("Failed to copy:", err);
      apiKeysError = "Could not copy to clipboard; select the key text and copy it manually.";
    }
  }

  function dismissGeneratedKey(): void {
    generatedKey = "";
  }

  // Fetches an existing key's plaintext value on demand and copies it. This
  // is a deliberate relaxation of "shown once at creation": any
  // authenticated user of this page can copy any key at any time, which
  // widens what a compromised Settings session can exfiltrate compared to
  // the creation-only exposure window used elsewhere in this feature.
  async function handleCopyExistingKey(entry: ApiKeyEntry): Promise<void> {
    apiKeysError = "";
    try {
      const key = await revealApiKey(entry.id);
      await copyTextToClipboard(key);
      copiedRowId = entry.id;
      setTimeout(() => (copiedRowId = ""), 1500);
    } catch (e) {
      apiKeysError = e instanceof Error ? e.message : String(e);
    }
  }
</script>

<div class="flex flex-col gap-4 h-full overflow-auto">
  <div class="card">
    <h3 class="mb-2">Access Control</h3>
    <p class="text-sm text-txtsecondary mb-4">
      {#if authStatus.enabled}
        This server is protected. Visitors must sign in with the username/password below.
      {:else}
        This server is <strong>not protected</strong>: anyone with network access can use it.
      {/if}
    </p>

    <div class="flex flex-col gap-2 max-w-md">
      <label class="text-sm" for="settings-username">Username</label>
      <input
        id="settings-username"
        type="text"
        class="px-3 py-1 rounded border border-gray-200 dark:border-white/10 bg-surface"
        bind:value={username}
        autocomplete="off"
      />

      <label class="text-sm" for="settings-password">New password</label>
      <input
        id="settings-password"
        type="password"
        class="px-3 py-1 rounded border border-gray-200 dark:border-white/10 bg-surface"
        bind:value={password}
        placeholder={authStatus.enabled ? "" : "new password"}
        autocomplete="new-password"
      />

      <label class="text-sm" for="settings-password-confirm">Confirm password</label>
      <input
        id="settings-password-confirm"
        type="password"
        class="px-3 py-1 rounded border border-gray-200 dark:border-white/10 bg-surface"
        bind:value={confirmPassword}
        autocomplete="new-password"
      />

      {#if authError}
        <p class="text-error text-sm">{authError}</p>
      {/if}

      <button class="btn text-base self-start" onclick={handleSaveAuth} disabled={authSaving}>
        {authSaving ? "Saving…" : "Save"}
      </button>
    </div>
  </div>

  <div class="card">
    <h3 class="mb-2">API Keys</h3>
    <p class="text-sm text-txtsecondary mb-4">
      Keys grant programmatic access (curl, SDKs) as an alternative to the username/password above.
    </p>

    {#if generatedKey}
      <div class="mb-4 p-3 rounded border border-warning bg-warning/10">
        <p class="text-sm font-semibold mb-1">Copy this key now: it won't be shown again.</p>
        <div class="flex items-center gap-2">
          <code class="flex-1 break-all">{generatedKey}</code>
          <button class="btn btn--sm" onclick={copyGeneratedKey}>{generatedKeyCopied ? "Copied!" : "Copy"}</button>
          <button class="btn btn--sm" onclick={dismissGeneratedKey}>Dismiss</button>
        </div>
      </div>
    {/if}

    <div class="flex items-center gap-2 mb-4">
      <input
        type="text"
        class="px-3 py-1 rounded border border-gray-200 dark:border-white/10 bg-surface"
        placeholder="Label (optional)"
        bind:value={newKeyLabel}
      />
      <button class="btn text-base" onclick={handleGenerateKey}>+ Generate key</button>
    </div>

    {#if apiKeysError}
      <p class="text-error text-sm mb-2">{apiKeysError}</p>
    {/if}

    <table class="w-full text-sm">
      <thead>
        <tr class="text-left text-txtsecondary">
          <th>Label</th>
          <th>Key</th>
          <th>Created</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        {#each apiKeys as entry, i (entry.id || `${entry.maskedKey}-${i}`)}
          <tr class="border-t border-card-border-inner">
            <td>{entry.label || "-"}</td>
            <td><code>{entry.maskedKey}</code></td>
            <td>{entry.createdAt ? new Date(entry.createdAt).toLocaleString() : "-"}</td>
            <td>
              {#if entry.id}
                <button class="btn btn--sm" onclick={() => handleCopyExistingKey(entry)}>
                  {copiedRowId === entry.id ? "Copied!" : "Copy"}
                </button>
                <button class="btn btn--sm" onclick={() => handleDeleteKey(entry)}>Delete</button>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</div>
