# Hardware-Fit Rating Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rate each GGUF file in Discover against the machine's RAM/VRAM and flag unsuitable model types before download.

**Architecture:** A new `GET /api/hub/hardware` endpoint exposes ramMB/vramMB/gpuName (gopsutil + perf monitor). A pure TypeScript module `modelFit.ts` turns file size + hardware into a rated badge; DiscoverPanel renders badges per file and a type-hint chip per repo. No LLM involved — deterministic arithmetic.

**Tech Stack:** Go (gopsutil/v4/mem, internal/perf), Svelte 5 + TypeScript, vitest.

**Verified facts:** `perf.Monitor.Current() ([]SysStat, []GpuStat)`; `GpuStat{Name string, MemTotalMB int}`; `s.perf` may be nil (perf disabled); existing hub handlers live in `internal/server/hubapi.go`; vitest tests live next to libs (`ui-svelte/src/lib/*.test.ts`); run `go test -run <pat> ./internal/server/`, `make test-dev`, `make test-ui`. Set `export PATH=$HOME/.local/go/bin:$PATH` for Go.

---

### Task 1: `GET /api/hub/hardware` endpoint

**Files:**
- Modify: `internal/server/hubapi.go` (append handler)
- Modify: `internal/server/server.go` (routes(), after the other /api/hub lines)
- Test: `internal/server/hubapi_test.go` (append)

- [x] **Step 1: Write the failing test** (append to `internal/server/hubapi_test.go`)

```go
func TestHubAPI_Hardware(t *testing.T) {
	srv := newHubTestServer(t, "models: {}\n")

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/hub/hardware", nil))
	assert.Equal(t, http.StatusOK, w.Code)

	var hw struct {
		RamMB   int    `json:"ramMB"`
		VramMB  int    `json:"vramMB"`
		GpuName string `json:"gpuName"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &hw))
	assert.Greater(t, hw.RamMB, 0)
	assert.GreaterOrEqual(t, hw.VramMB, 0)
}
```

Add `"encoding/json"` to the test file imports.

- [x] **Step 2: Run test to verify it fails**

Run: `go test -run TestHubAPI_Hardware -v ./internal/server/`
Expected: FAIL (404 — route not registered)

- [x] **Step 3: Implement** (append to `internal/server/hubapi.go`)

```go
// handleHubHardware reports the machine's memory so the UI can rate model
// fit. Not gated on modelsDir: it is plain hardware info.
func (s *Server) handleHubHardware(w http.ResponseWriter, r *http.Request) {
	ramMB := 0
	if vm, err := mem.VirtualMemory(); err == nil {
		ramMB = int(vm.Total / (1024 * 1024))
	}
	vramMB := 0
	gpuName := ""
	if s.perf != nil {
		_, gpuStats := s.perf.Current()
		for _, g := range gpuStats {
			if g.MemTotalMB > vramMB {
				vramMB = g.MemTotalMB
				gpuName = g.Name
			}
		}
	}
	sendJSON(w, map[string]any{
		"ramMB":   ramMB,
		"vramMB":  vramMB,
		"gpuName": gpuName,
	})
}
```

Add import `"github.com/shirou/gopsutil/v4/mem"` to `hubapi.go`.

In `internal/server/server.go` `routes()`, after the `/api/hub/delete` line:

```go
	mux.Handle("GET /api/hub/hardware", apiChain.ThenFunc(s.handleHubHardware))
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test -run TestHubAPI_Hardware -v ./internal/server/`
Expected: PASS

- [x] **Step 5: Commit**

```bash
gofmt -w internal/server/
git add internal/server/
git commit -m "server: add /api/hub/hardware endpoint for model fit rating"
```

---

### Task 2: `modelFit.ts` heuristic with vitest coverage

**Files:**
- Create: `ui-svelte/src/lib/modelFit.ts`
- Test: `ui-svelte/src/lib/modelFit.test.ts`

- [x] **Step 1: Write the failing tests** (`ui-svelte/src/lib/modelFit.test.ts`)

```ts
import { describe, expect, it } from "vitest";
import { rateModelFit, typeHint } from "./modelFit";

const GB = 1024 ** 3;

describe("rateModelFit", () => {
  const ramOnly = { ramMB: 32_000, vramMB: 0 };
  const withGpu = { ramMB: 64_000, vramMB: 24_000 };

  it("rates great when it fits in VRAM", () => {
    const fit = rateModelFit(10 * GB, withGpu);
    expect(fit.level).toBe("great");
    expect(fit.label).toBe("Great fit");
    expect(fit.reason).toContain("VRAM");
  });

  it("rates good when it fits in RAM (no GPU stats)", () => {
    const fit = rateModelFit(10 * GB, ramOnly);
    expect(fit.level).toBe("good");
    expect(fit.reason).toContain("RAM");
  });

  it("rates good when too big for VRAM but fits in RAM", () => {
    expect(rateModelFit(30 * GB, withGpu).level).toBe("good");
  });

  it("rates tight near the RAM ceiling", () => {
    // 32000 MB RAM: good cutoff 25600 MB, tight cutoff 30400 MB.
    // 24 GB file -> needMB = 24*1024*1.15 = 28262 -> tight
    expect(rateModelFit(24 * GB, ramOnly).level).toBe("tight");
  });

  it("rates too-large beyond RAM", () => {
    expect(rateModelFit(28 * GB, ramOnly).level).toBe("too-large");
  });

  it("returns unknown for zero/negative sizes", () => {
    expect(rateModelFit(0, ramOnly).level).toBe("unknown");
    expect(rateModelFit(-5, ramOnly).level).toBe("unknown");
  });

  it("returns unknown when hardware is missing", () => {
    expect(rateModelFit(GB, null).level).toBe("unknown");
    expect(rateModelFit(GB, { ramMB: 0, vramMB: 0 }).level).toBe("unknown");
  });
});

describe("typeHint", () => {
  it("flags embedding models", () => {
    expect(typeHint("feature-extraction")).toContain("embedding");
    expect(typeHint("sentence-similarity")).toContain("embedding");
  });
  it("flags vision models", () => {
    expect(typeHint("image-text-to-text")).toContain("mmproj");
  });
  it("flags image generation models", () => {
    expect(typeHint("text-to-image")).toContain("stable-diffusion");
  });
  it("is empty for text-generation and unknown tags", () => {
    expect(typeHint("text-generation")).toBe("");
    expect(typeHint("")).toBe("");
    expect(typeHint("automatic-speech-recognition")).toBe("");
  });
});
```

- [x] **Step 2: Run tests to verify they fail**

Run: `cd ui-svelte && npx vitest run src/lib/modelFit.test.ts`
Expected: FAIL (module not found)

- [x] **Step 3: Implement** (`ui-svelte/src/lib/modelFit.ts`)

```ts
// Deterministic hardware-fit heuristic for GGUF files in Discover.
// Estimated runtime need = file size * 1.15 (weights + overhead); the KV
// cache for long contexts is extra, which the reason text mentions.

export type FitLevel = "great" | "good" | "tight" | "too-large" | "unknown";

export interface Hardware {
  ramMB: number;
  vramMB: number;
  gpuName?: string;
}

export interface ModelFit {
  level: FitLevel;
  label: string;
  reason: string;
}

const OVERHEAD = 1.15;

function gb(mb: number): string {
  return `${(mb / 1024).toFixed(1)} GB`;
}

export function rateModelFit(sizeBytes: number, hw: Hardware | null): ModelFit {
  if (!hw || sizeBytes <= 0 || hw.ramMB <= 0) {
    return { level: "unknown", label: "", reason: "" };
  }
  const needMB = (sizeBytes / 1024 ** 2) * OVERHEAD;
  const suffix = "(long contexts need extra memory for the KV cache)";

  if (hw.vramMB > 0 && needMB <= hw.vramMB * 0.9) {
    return {
      level: "great",
      label: "Great fit",
      reason: `~${gb(needMB)} needed, fits in ${gb(hw.vramMB)} VRAM — fast ${suffix}`,
    };
  }
  if (needMB <= hw.ramMB * 0.8) {
    return {
      level: "good",
      label: "Good fit",
      reason: `~${gb(needMB)} needed of ${gb(hw.ramMB)} RAM — runs with CPU/partial offload ${suffix}`,
    };
  }
  if (needMB <= hw.ramMB * 0.95) {
    return {
      level: "tight",
      label: "Tight",
      reason: `~${gb(needMB)} needed of ${gb(hw.ramMB)} RAM — barely fits, likely slow or swapping`,
    };
  }
  return {
    level: "too-large",
    label: "Too large",
    reason: `~${gb(needMB)} needed but only ${gb(hw.ramMB)} RAM — exceeds this machine's memory`,
  };
}

export function typeHint(pipelineTag: string): string {
  switch (pipelineTag) {
    case "feature-extraction":
    case "sentence-similarity":
      return "embedding model — not for chat";
    case "image-text-to-text":
      return "vision model — may need an mmproj file";
    case "text-to-image":
      return "image generation — needs stable-diffusion.cpp";
    default:
      return "";
  }
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `cd ui-svelte && npx vitest run src/lib/modelFit.test.ts`
Expected: PASS (11 tests)

- [x] **Step 5: Commit**

```bash
git add ui-svelte/src/lib/modelFit.ts ui-svelte/src/lib/modelFit.test.ts
git commit -m "ui: add hardware-fit heuristic for GGUF files"
```

---

### Task 3: Wire into DiscoverPanel

**Files:**
- Modify: `ui-svelte/src/stores/api.ts` (hardware fetch helper + Hardware type import source is modelFit.ts)
- Modify: `ui-svelte/src/components/DiscoverPanel.svelte`

- [x] **Step 1: Add fetch helper** (append near other hub helpers in `ui-svelte/src/stores/api.ts`)

```ts
export async function hubHardware(): Promise<Hardware> {
  return hubFetch<Hardware>(`/api/hub/hardware`);
}
```

Add `import type { Hardware } from "../lib/modelFit";` at the top.

- [x] **Step 2: Render badges in `DiscoverPanel.svelte`**

Script additions:

```ts
  import { hubHardware } from "../stores/api";   // merge into existing api import line is fine
  import { rateModelFit, typeHint, type Hardware } from "../lib/modelFit";

  let hardware = $state<Hardware | null>(null);

  // inside the existing $effect that calls loadRepos(), or a new one:
  $effect(() => {
    hubHardware()
      .then((hw) => (hardware = hw))
      .catch(() => (hardware = null)); // no badges when unavailable
  });
```

Badge colors, file row: in the expanded-files table, change the action cell to
include the fit badge before the Download/Downloaded markup:

```svelte
<td class="w-56 text-right whitespace-nowrap">
  {#if rateModelFit(file.size, hardware).level !== "unknown"}
    {@const fit = rateModelFit(file.size, hardware)}
    <span
      class="text-xs px-1.5 py-0.5 rounded mr-2
        {fit.level === 'great' ? 'bg-green-600/20 text-green-600 dark:text-green-400' : ''}
        {fit.level === 'good' ? 'bg-teal-600/20 text-teal-700 dark:text-teal-400' : ''}
        {fit.level === 'tight' ? 'bg-amber-500/20 text-amber-700 dark:text-amber-400' : ''}
        {fit.level === 'too-large' ? 'bg-red-600/20 text-red-600 dark:text-red-400' : ''}"
      title={fit.reason}>{fit.label}</span>
  {/if}
  ... existing Downloaded ✓ / downloading / Download button markup ...
</td>
```

Repo card type hint, next to the pipeline-tag chip in the meta line:

```svelte
{#if typeHint(repo.pipelineTag)}
  <span class="px-1.5 py-0.5 rounded border border-amber-500/50 text-amber-700 dark:text-amber-400">
    {typeHint(repo.pipelineTag)}
  </span>
{/if}
```

- [x] **Step 3: Verify**

Run: `cd ui-svelte && npm run check && npm test`
Expected: 0 errors, all tests pass

- [x] **Step 4: Commit**

```bash
git add ui-svelte/src
git commit -m "ui: show hardware-fit badges and type hints in Discover"
```

---

### Task 4: Final verification + deploy

- [x] **Step 1:** `make test-dev` → all pass (ignore pre-existing internal/perf staticcheck note)
- [x] **Step 2:** `make test-ui` equivalent already run in Task 3; run `cd ui-svelte && npm test` if not
- [x] **Step 3:** `make linux-amd64`, stop service, copy `build/llama-swap-linux-amd64` to `~/.local/bin/llama-swap`, start service, `curl -s http://127.0.0.1:8080/api/hub/hardware`
- [x] **Step 4:** Commit any leftovers; verify `git status` clean
