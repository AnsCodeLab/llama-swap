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
  const est = `Estimated ~${gb(needMB)} needed (file size + 15% overhead).`;

  if (hw.vramMB > 0 && needMB <= hw.vramMB * 0.9) {
    return {
      level: "great",
      label: "Great fit",
      reason: `${est} Fits within 90% of your ${gb(hw.vramMB)} VRAM, so the whole model can be offloaded to the GPU for fast inference. Long contexts add KV-cache memory on top.`,
    };
  }
  if (needMB <= hw.ramMB * 0.8) {
    return {
      level: "good",
      label: "Good fit",
      reason: `${est} Uses under 80% of your ${gb(hw.ramMB)} RAM, leaving headroom to run with CPU or partial GPU offload. Long contexts add KV-cache memory on top.`,
    };
  }
  if (needMB <= hw.ramMB * 0.95) {
    return {
      level: "tight",
      label: "Tight",
      reason: `${est} Uses 80–95% of your ${gb(hw.ramMB)} RAM — it may load, but expect swapping and slow generation, with little room left for the KV cache.`,
    };
  }
  return {
    level: "too-large",
    label: "Too large",
    reason: `${est} Exceeds 95% of your ${gb(hw.ramMB)} RAM — it will not fit in memory on this machine. Pick a smaller quantization of this model instead.`,
  };
}

const TOOL_TAGS = new Set(["tool-calling", "function-calling", "text-generation-inference:tools"]);

// Heuristic only: relies on HF tags set by the uploader, so a model can
// support tool calling without being tagged for it.
export function supportsToolCalling(tags: string[]): boolean {
  return tags.some((t) => TOOL_TAGS.has(t.toLowerCase()));
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
