export function formatBytes(bytes) {
  if (bytes === 0) return "0 B";
  if (!bytes) return "—";
  const units = ["B", "KiB", "MiB", "GiB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return (unit === 0 ? value : value.toFixed(1)) + " " + units[unit];
}

export function formatDuration(ms) {
  if (ms === null || ms === undefined) return "—";
  // Every tool here makes a network call, so sub-millisecond timings only
  // happen for calls that failed before leaving the process — worth showing
  // as "<1ms" rather than a bare "0ms" that reads like a broken clock.
  if (ms < 1) return "<1ms";
  if (ms < 1000) return ms + "ms";
  return (ms / 1000).toFixed(ms < 10000 ? 2 : 1) + "s";
}

export function formatTime(iso) {
  const date = new Date(iso);
  if (isNaN(date.getTime())) return iso;
  return date.toLocaleString();
}

// prettyJSON reformats a stored JSON payload for display, falling back to the
// raw string: a truncated preview is valid text but usually invalid JSON, so
// parsing it must never be what decides whether the payload is shown at all.
export function prettyJSON(raw) {
  if (!raw) return "";
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch (err) {
    return raw;
  }
}
