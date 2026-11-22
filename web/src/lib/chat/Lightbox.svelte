<script lang="ts">
  // Full-screen image viewer; opened by delegated img clicks in Markdown.
  import { lightbox, closeLightbox } from './state.svelte.js';
  import Modal from './ui/Modal.svelte';
</script>

<Modal open={!!lightbox.src} onclose={closeLightbox}>
  {#if lightbox.src}
    <img
      src={lightbox.src}
      alt="Attachment preview"
      class="max-h-[85vh] max-w-[90vw] rounded-xl object-contain"
      style="border:1px solid var(--border)"
    />
  {/if}
</Modal>
