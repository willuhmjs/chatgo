<script lang="ts">
  // Full-screen image viewer; opened by delegated img clicks in Markdown.
  // Chrome stays quiet (spec §7): the image floats on the dark scrim alone.
  import { lightbox, closeLightbox } from './state.svelte.js';
  import Modal from './ui/Modal.svelte';
</script>

<Modal open={!!lightbox.src} onclose={closeLightbox}>
  {#if lightbox.src}
    <img
      src={lightbox.src}
      alt="Attachment preview"
      class="max-h-[85vh] max-w-[90vw] rounded-2xl object-contain"
      style="box-shadow: var(--shadow-menu, 0 8px 28px rgb(0 0 0 / 0.45))"
    />
  {/if}
</Modal>
