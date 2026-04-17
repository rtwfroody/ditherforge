<script lang="ts">
  import * as Tooltip from '$lib/components/ui/tooltip';
  import { InfoIcon } from '@lucide/svelte';
  import type { Snippet } from 'svelte';

  let {
    children,
    side = 'top',
  }: {
    children: Snippet;
    side?: 'top' | 'right' | 'bottom' | 'left';
  } = $props();
</script>

<Tooltip.Root>
  <Tooltip.Trigger
    type="button"
    tabindex={-1}
    class="inline-flex items-center justify-center text-muted-foreground hover:text-foreground cursor-help align-middle"
    aria-label="Help"
  >
    <InfoIcon size={13} />
  </Tooltip.Trigger>
  <Tooltip.Content {side}>
    {@render children()}
  </Tooltip.Content>
</Tooltip.Root>
