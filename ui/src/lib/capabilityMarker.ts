// Helpers for the "# llama-swap auto" pending marker that capabilities
// write-back writes above a model's capabilities: block in the config file.
// The marker line records, per auto-filled field, the value it wrote so the
// UI can tell an auto line apart from a user comment and the server can tell
// an edited field apart from an untouched one.

// Matches a marker line anywhere its leading "#" sits, with optional
// indentation, e.g. "    # llama-swap auto (2026-02-14) in=[text] out=[text]".
const AUTO_MARKER_RE = /^ *# *llama-swap auto /;

// Returns true when the line is a llama-swap auto marker line.
export function isAutoMarkerLine(line: string): boolean {
  return AUTO_MARKER_RE.test(line);
}

export interface ParsedAutoMarker {
  date?: string;
  fields: string[];
}

// Parses a marker line into its recorded date and field names. Returns null
// when the line is not a marker line.
export function parseAutoMarkerLine(line: string): ParsedAutoMarker | null {
  if (!isAutoMarkerLine(line)) return null;
  // Strip the leading "# " comment prefix (and any indentation) to reach the
  // "llama-swap auto (...)" payload.
  const payload = line.replace(/^ *# */, "");
  const fields: string[] = [];

  let date: string | undefined;
  const dateMatch = payload.match(/\((\d{4}-\d{2}-\d{2})\)/);
  if (dateMatch) date = dateMatch[1];

  // Field values follow as "name=value" tokens, where a value may itself
  // contain "=" (it never contains whitespace), e.g. in=[text,image].
  for (const token of payload.split(/\s+/)) {
    if (token.startsWith("llama-swap") || token === "auto" || token.startsWith("(")) continue;
    const eq = token.indexOf("=");
    if (eq > 0) fields.push(token.slice(0, eq));
  }

  return { date, fields };
}
