<script lang="ts">
  let {
    w = '100%',
    h = '0.75rem',
    rounded = '0.375rem',
  }: {
    w?: string;
    h?: string;
    rounded?: string;
  } = $props();
</script>

<span class="skel" style="width:{w}; height:{h}; border-radius:{rounded}"></span>

<style>
  .skel {
    display: block;
    background: color-mix(in srgb, var(--text) 8%, transparent);
    animation: skel-pulse 1.6s ease-in-out infinite;
  }
  @keyframes skel-pulse {
    0%,
    100% {
      opacity: 1;
    }
    50% {
      opacity: 0.45;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .skel {
      animation: none;
    }
  }
</style>
