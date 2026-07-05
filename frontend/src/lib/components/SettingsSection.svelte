<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    title,
    open = true,
    variant = 'top',
    tip,
    children,
  }: {
    title: string;
    open?: boolean;
    variant?: 'top' | 'sub';
    tip?: Snippet;
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
