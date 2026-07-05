<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    title,
    open = true,
    variant = 'top',
    tip,
    summary,
    children,
  }: {
    title: string;
    open?: boolean;
    variant?: 'top' | 'sub';
    tip?: Snippet;
    // Muted one-line state recap shown to the right of the title in the
    // header, whether the section is open or closed. String or snippet;
    // truncated with an ellipsis when it doesn't fit. Top variant only.
    summary?: Snippet | string;
    children: Snippet;
  } = $props();
</script>

<details class="section" {open}>
  {#if variant === 'sub'}
    <summary class="flex items-center gap-2 text-xs font-medium text-muted-foreground cursor-pointer select-none">
      <span class="section-chevron inline-block transition-transform">▸</span>
      <span>{title}</span>
      {#if tip}{@render tip()}{/if}
    </summary>
    <div class="mt-2 ml-1 pl-3 border-l border-border">
      {@render children()}
    </div>
  {:else}
    <summary class="flex items-center gap-2 text-[11px] font-semibold uppercase tracking-widest text-muted-foreground cursor-pointer select-none">
      <span class="section-chevron inline-block transition-transform">▸</span>
      <span>{title}</span>
      {#if tip}{@render tip()}{/if}
      {#if summary}
        <span class="min-w-0 truncate text-xs font-normal normal-case tracking-normal text-muted-foreground">
          {#if typeof summary === 'string'}{summary}{:else}{@render summary()}{/if}
        </span>
      {/if}
      <div class="flex-1 h-px bg-border"></div>
    </summary>
    <div class="mt-3">
      {@render children()}
    </div>
  {/if}
</details>

<style>
  details.section > summary {
    list-style: none;
  }
  details.section > summary::-webkit-details-marker {
    display: none;
  }
  details.section[open] > summary .section-chevron {
    transform: rotate(90deg);
  }
</style>
