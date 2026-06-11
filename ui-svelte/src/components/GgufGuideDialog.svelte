<script lang="ts">
  interface Props {
    open: boolean;
    onclose: () => void;
  }

  let { open, onclose }: Props = $props();

  let dialogEl: HTMLDialogElement | undefined = $state();

  $effect(() => {
    if (open && dialogEl) {
      dialogEl.showModal();
    } else if (!open && dialogEl) {
      dialogEl.close();
    }
  });

  const quants = [
    { code: "F32 / F16 / BF16", bits: "32 / 16", quality: "Original (no quantization)", note: "Huge files; only for conversion or small models" },
    { code: "Q8_0", bits: "8", quality: "Near-lossless", note: "~2× smaller than F16; rarely worth it over Q6_K" },
    { code: "Q6_K", bits: "~6.6", quality: "Excellent", note: "Practically indistinguishable from Q8_0" },
    { code: "Q5_K_M", bits: "~5.7", quality: "Very good", note: "Good middle ground" },
    { code: "Q4_K_M", bits: "~4.8", quality: "Good — most popular", note: "Best size/quality trade-off for most users" },
    { code: "Q4_K_S", bits: "~4.6", quality: "Good", note: "Slightly smaller, slightly worse than _M" },
    { code: "IQ4_XS", bits: "~4.3", quality: "Good", note: "Importance-matrix quant; smaller at similar quality" },
    { code: "Q3_K_M / IQ3_M", bits: "~3.5", quality: "Noticeable loss", note: "Use when memory is tight" },
    { code: "Q2_K / IQ2_M", bits: "~2.8", quality: "Heavy loss", note: "Last resort for very large models" },
  ];
</script>

<dialog
  bind:this={dialogEl}
  onclose={() => onclose()}
  class="bg-surface text-txtmain rounded-lg shadow-xl max-w-3xl w-full max-h-[90vh] p-0 backdrop:bg-black/50 m-auto"
>
  <div class="flex flex-col max-h-[90vh]">
    <div class="flex justify-between items-center p-4 border-b border-card-border">
      <h2 class="text-xl font-bold pb-0">How to read GGUF model names</h2>
      <button onclick={() => dialogEl?.close()} class="text-txtsecondary hover:text-txtmain text-2xl leading-none">
        &times;
      </button>
    </div>

    <div class="overflow-y-auto flex-1 p-4 space-y-5 text-sm">
      <section>
        <h3 class="font-semibold mb-2">Anatomy of a filename</h3>
        <p class="font-mono text-xs bg-card border border-card-border rounded p-2 break-all">
          Qwen3-Coder-<span class="text-green-600 dark:text-green-400">30B</span>-<span class="text-teal-600 dark:text-teal-400">A3B</span>-<span class="text-amber-600 dark:text-amber-400">Instruct</span>-<span class="text-purple-600 dark:text-purple-400">UD</span>-<span class="text-red-600 dark:text-red-400">Q4_K_XL</span>.gguf
        </p>
        <ul class="list-disc pl-5 mt-2 space-y-1">
          <li><strong>Qwen3-Coder</strong> — model family and specialization (Coder = tuned for programming).</li>
          <li><strong class="text-green-600 dark:text-green-400">30B</strong> — parameter count: 30 billion. More parameters → smarter but bigger and slower. 1B–4B: light tasks; 7B–14B: solid general use; 30B+: strong reasoning if your memory allows.</li>
          <li><strong class="text-teal-600 dark:text-teal-400">A3B</strong> — Mixture-of-Experts (MoE): 30B total parameters but only ~3B <em>active</em> per token. Needs the memory of a 30B model but runs at the speed of a 3B one.</li>
          <li><strong class="text-amber-600 dark:text-amber-400">Instruct</strong> — fine-tuning variant (see below).</li>
          <li><strong class="text-purple-600 dark:text-purple-400">UD</strong> — quantizer's mark: Unsloth Dynamic quants. Repo owners like <em>bartowski</em>, <em>unsloth</em>, <em>mradermacher</em> publish quantized versions of other people's models.</li>
          <li><strong class="text-red-600 dark:text-red-400">Q4_K_XL</strong> — quantization level (see table below).</li>
        </ul>
      </section>

      <section>
        <h3 class="font-semibold mb-2">Variant tags</h3>
        <ul class="list-disc pl-5 space-y-1">
          <li><strong>Instruct / Chat / IT</strong> — fine-tuned to follow instructions. This is what you want for chat and agents.</li>
          <li><strong>Base</strong> — raw pretrained model, completes text but doesn't follow instructions. For fine-tuning, not chatting.</li>
          <li><strong>Thinking / Reasoning / R1 / Distill</strong> — trained to emit reasoning steps before answering. Slower per answer, better at hard problems.</li>
          <li><strong>Coder / Code</strong> — specialized for programming.</li>
          <li><strong>VL / Vision</strong> — accepts images; usually needs a separate <code>mmproj-*.gguf</code> file from the same repo, passed with <code>--mmproj</code>.</li>
          <li><strong>2507 / 0528 …</strong> — release date stamps (year-month or month-day) distinguishing model revisions.</li>
        </ul>
      </section>

      <section>
        <h3 class="font-semibold mb-2">Quantization: quality vs size</h3>
        <p class="mb-2">
          Quantization shrinks the model by storing weights with fewer bits. The code reads as
          <code>Q&lt;bits&gt;_&lt;method&gt;_&lt;size&gt;</code>: <strong>K</strong> = modern "k-quant" method,
          <strong>IQ</strong> = importance-matrix quants (better at low bits),
          <strong>S / M / L / XL</strong> = small→extra-large variants within the same bit level (bigger = better quality).
        </p>
        <table class="w-full text-xs border border-card-border">
          <thead>
            <tr class="text-left bg-card border-b border-card-border">
              <th class="p-1.5">Code</th>
              <th class="p-1.5">Bits/weight</th>
              <th class="p-1.5">Quality</th>
              <th class="p-1.5">Notes</th>
            </tr>
          </thead>
          <tbody>
            {#each quants as q (q.code)}
              <tr class="border-b border-card-border align-top">
                <td class="p-1.5 font-mono whitespace-nowrap">{q.code}</td>
                <td class="p-1.5">{q.bits}</td>
                <td class="p-1.5">{q.quality}</td>
                <td class="p-1.5">{q.note}</td>
              </tr>
            {/each}
          </tbody>
        </table>
        <p class="mt-2 text-txtsecondary">
          Rule of thumb: a file needs roughly its own size in memory (+15% overhead, plus KV cache for long
          contexts). Pick the largest quant that still shows a <span class="text-teal-600 dark:text-teal-400 font-semibold">Good fit</span> badge —
          Q4_K_M is the sweet spot for most models.
        </p>
      </section>

      <section>
        <h3 class="font-semibold mb-2">Other things you'll see</h3>
        <ul class="list-disc pl-5 space-y-1">
          <li><strong>-00001-of-00003</strong> — one model split into multiple files. llama-swap downloads all parts automatically; llama.cpp loads them given the first part.</li>
          <li><strong>mmproj-F16.gguf</strong> — the vision projector for VL models. Not a standalone model.</li>
          <li><strong>GGUF</strong> — the file format llama.cpp uses: weights + tokenizer + chat template in one file.</li>
        </ul>
      </section>
    </div>
  </div>
</dialog>
