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
