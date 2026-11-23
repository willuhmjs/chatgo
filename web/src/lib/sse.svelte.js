// Thin reactive wrapper over api.streamMessage (scaffold-owned, contract
// lists lib/sse.svelte.js). FE-chat consumes `stream` state + callbacks.
import { streamMessage, streamRegenerate } from './api';

export const stream = $state({ active: false, error: null });

let controller = null;

/**
 * Start a message stream. `handlers` receives parsed SSE events:
 * { event: 'meta'|'title'|'sources'|'token'|'done'|'error', data }.
 * @returns {Promise<void>} resolves when the stream terminates.
 */
export async function send(chatID, body, handlers) {
  return run(handlers, (onEvent, signal) => streamMessage(chatID, body, onEvent, signal));
}

export async function regenerate(chatID, fromMsgID, body, handlers) {
  return run(handlers, (onEvent, signal) => streamRegenerate(chatID, fromMsgID, body, onEvent, signal));
}

export function abort() {
  controller?.abort();
  controller = null;
  stream.active = false;
}

async function run(handlers, start) {
  controller = new AbortController();
  stream.active = true;
  stream.error = null;
  try {
    await start((ev) => handlers?.[ev.event]?.(ev.data), controller.signal);
  } catch (err) {
    if (err?.name !== 'AbortError') stream.error = String(err?.message ?? err);
  } finally {
    stream.active = false;
    controller = null;
  }
}
