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
