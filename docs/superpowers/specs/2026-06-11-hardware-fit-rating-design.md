# Hardware-Fit Rating for Discover — Design

Date: 2026-06-11
Status: Approved (user standing instruction: process autonomously)

## Goal

In the Discover section, rate each GGUF file against the current machine's
memory and the repo's model type, so users know before downloading whether a
model is too large or the wrong kind for their hardware.

## Decisions

- **Deterministic heuristic, not an LLM call.** An LLM rating would require a
  loaded model (circular), be slow per file, and add nothing over arithmetic.
- **Computed client-side** from one new hardware endpoint; the heuristic is a
  pure TypeScript function with vitest coverage.
- **Advisory, not blocking**: "Too large" files keep their Download button.
- **RAM-first**: on machines without discrete-GPU stats (e.g. Intel iGPU with
  shared memory) the budget is system RAM; VRAM upgrades the rating when the
  perf monitor reports it.

## Backend

`GET /api/hub/hardware` (apiChain, in `internal/server/hubapi.go`):

```json
{ "ramMB": 31548, "vramMB": 0, "gpuName": "" }
```

- `ramMB`: gopsutil `mem.VirtualMemory().Total` / MiB.
- `vramMB`, `gpuName`: from the newest sample of `s.perf.Current()` GPU stats
  (`MemTotalMB`, `Name`); zero values when perf is disabled or no GPU stats
  exist. Multiple GPUs: use the largest `MemTotalMB`.
- The endpoint is not gated on `modelsDir` or the hub manager — it is plain
  hardware info, available whenever the server runs.

## Fit heuristic (`ui-svelte/src/lib/modelFit.ts`)

```ts
rateModelFit(sizeBytes, hw: {ramMB, vramMB}): {level, label, reason}
```

- `needMB = sizeBytes / 1MiB * 1.15` (weights + runtime overhead; KV cache for
  long contexts is extra — mentioned in the reason text).
- Levels, first match wins:
  - `vramMB > 0 && needMB <= vramMB * 0.9` → `great` "Great fit" — fits in VRAM, fast
  - `needMB <= ramMB * 0.8` → `good` "Good fit" — fits in RAM (CPU or partial offload)
  - `needMB <= ramMB * 0.95` → `tight` "Tight" — barely fits, likely slow/swapping
  - else → `too-large` "Too large" — exceeds this machine's memory
- Reason string includes the numbers, e.g. "~7.2 GB needed of 30.8 GB RAM".
- `sizeBytes <= 0` → `unknown` level, no badge.

Type hint (`typeHint(pipelineTag)` in the same module):

| pipelineTag | hint |
|---|---|
| `feature-extraction`, `sentence-similarity` | "embedding model — not for chat" |
| `image-text-to-text` | "vision model — may need an mmproj file" |
| `text-to-image` | "image generation — needs stable-diffusion.cpp" |
| `text-generation`, empty, anything else | none |

## UI (DiscoverPanel)

- Fetch `/api/hub/hardware` once on mount (failure → no badges, no errors).
- File rows: colored badge before the Download button — green `Great fit`,
  teal `Good fit`, amber `Tight`, red `Too large` — `title` tooltip carries
  the reason.
- Repo cards: when `typeHint` is non-empty, an amber-ish chip with the hint
  next to the pipeline-tag chip.

## Testing

- Go: `TestHubAPI_Hardware` — endpoint returns ramMB > 0 and JSON shape.
- vitest: `modelFit.test.ts` — each level boundary, VRAM vs RAM-only paths,
  unknown size, type hints.
- `make test-dev`, `make test-ui`.
