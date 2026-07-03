// Pure conversions for the time-of-day / date control (#113): the slider works in a
// date string + minutes-of-day (0..1439), while /stream's `start` knob wants a single
// RFC3339 UTC instant. These fold the two representations together, and format an
// instant for a human-readable label (the picker's caption and the running sim clock).
//
// Everything is done by string surgery on the fixed `YYYY-MM-DDTHH:MM:00Z` shape — no
// `new Date()` — so the result is timezone-independent and deterministically testable.

/** Minutes in a day; the time-of-day slider spans 0..MINUTES_IN_DAY-1. */
export const MINUTES_IN_DAY = 24 * 60;

const MONTHS = [
  "Jan", "Feb", "Mar", "Apr", "May", "Jun",
  "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
];

/** Zero-pad a non-negative integer to two digits. */
function pad2(n: number): string {
  return String(n).padStart(2, "0");
}

/** Wrap any integer minute count into [0, MINUTES_IN_DAY) — negative-safe. */
function wrapMinutes(minutes: number): number {
  return ((Math.trunc(minutes) % MINUTES_IN_DAY) + MINUTES_IN_DAY) % MINUTES_IN_DAY;
}

/**
 * Combine a `YYYY-MM-DD` date and a minutes-of-day count into an RFC3339 UTC instant
 * `YYYY-MM-DDTHH:MM:00Z`. Minutes are wrapped into [0, MINUTES_IN_DAY) so a stray
 * out-of-range value can't produce an hour past 23.
 */
export function combineSimStart(date: string, minutes: number): string {
  const wrapped = wrapMinutes(minutes);
  const hh = Math.floor(wrapped / 60);
  const mm = wrapped % 60;
  return `${date}T${pad2(hh)}:${pad2(mm)}:00Z`;
}

/**
 * Split an RFC3339 instant back into the `{ date, minutes }` the control edits. Only the
 * date and HH:MM are read; seconds are dropped (the slider's resolution is a minute).
 * An unparseable string yields an empty date and midnight so the UI degrades gracefully.
 */
export function splitSimStart(rfc: string): { date: string; minutes: number } {
  const m = /^(\d{4}-\d{2}-\d{2})T(\d{2}):(\d{2})/.exec(rfc);
  if (!m) return { date: "", minutes: 0 };
  return { date: m[1], minutes: Number(m[2]) * 60 + Number(m[3]) };
}

/** Format a minutes-of-day count as a 24-hour `HH:MM` clock (the slider's live value). */
export function formatMinutes(minutes: number): string {
  const wrapped = wrapMinutes(minutes);
  return `${pad2(Math.floor(wrapped / 60))}:${pad2(wrapped % 60)}`;
}

/**
 * Format an RFC3339 instant as a readable UTC label, e.g. `Jun 22, 2026 · 08:00 UTC`.
 * Used both for the picker's selected-instant caption and the running sim clock. An
 * unparseable string is returned verbatim so we never render "Invalid Date".
 */
export function formatInstant(rfc: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})/.exec(rfc);
  if (!m) return rfc;
  const [, y, mo, d, hh, mm] = m;
  const month = MONTHS[Number(mo) - 1] ?? mo;
  return `${month} ${Number(d)}, ${y} · ${hh}:${mm} UTC`;
}
