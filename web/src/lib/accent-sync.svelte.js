// Accent boot sync: mirrors the me-settings "accent" key into $lib/accent
// after sign-in and migrates a device-only choice upward once. Precedence:
// server > localStorage > code default (blue) — a first-time visitor never
// reaches the server branch at all, they already render blue from initAccent.
// Its own small fetch: model-prefs' load() consumes the shared GET payload
// for two unrelated keys and App.svelte's onboarding probe is admin-only,
// so neither can hand us the value. Idempotent; failures allow a retry.
import { getMeSettings, setMeSettings } from './api';
import {
  ACCENTS,
  isAccentMigrationPending,
  markAccentMigrated,
  readStoredAccent,
  setAccent,
  syncAccentFromServer,
} from './accent.svelte.js';

const SERVER_KEY = 'accent';

let started = false;

/** Contract: a JSON/plain string like model-prefs' keys; tolerate quotes.
    Values outside ACCENTS (e.g. a legacy hex) are ignored. */
function parseAccent(/** @type {unknown} */ raw) {
  if (typeof raw !== 'string') return '';
  let v = raw.trim();
  if (v.startsWith('"')) {
    try {
      const inner = JSON.parse(v);
      if (typeof inner === 'string') v = inner.trim();
    } catch {
      /* keep raw */
    }
  }
  return ACCENTS.includes(/** @type {any} */ (v)) ? v : '';
}

/** Fire-and-forget; call once the session has a user. */
export function bootAccent() {
  if (started) return;
  started = true;
  void getMeSettings()
    .then((s) => {
      const name = parseAccent(s?.[SERVER_KEY]);
      if (name) {
        // Server wins over a stale local choice.
        syncAccentFromServer(name);
        return;
      }
      // Account has no accent (new or older server): if this device carries an
      // explicit local choice, adopt it and push it up once. Exception: the
      // OLD default 'violet' with no migration flag is treated as implicit —
      // v2 shipped violet as the default AND the pre-highlighted swatch, so a
      // stored violet on a never-configured account is more likely "never
      // chose" than "chose violet". Flip to blue, persist, push up, and mark
      // the flag (see isAccentMigrationPending for the honest trade-off).
      const local = readStoredAccent();
      if (!local) return; // nothing stored — first-time visitor, already blue
      if (isAccentMigrationPending()) {
        markAccentMigrated();
        setAccent('blue'); // persists locally + write-through to the server
        return;
      }
      void setMeSettings({ [SERVER_KEY]: local }).catch(() => {});
    })
    .catch(() => {
      started = false; // offline / older server — local choice stands, retry later
    });
}
