export interface ListenerInfo {
  name: string;
  listen: string;
  models: string[];
  bound?: boolean;
  error?: string;
}

async function requestJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, init);
  if (!response.ok) {
    let message = `${response.status}`;
    try {
      // Errors use the OpenAI-compatible envelope: {error: {message, ...}}.
      const body = (await response.json()) as { error?: { message?: string } | string };
      const detail = body.error;
      const text = typeof detail === "string" ? detail : detail?.message;
      if (text) message = text;
    } catch {
      /* keep status code */
    }
    throw new Error(message);
  }
  return (await response.json()) as T;
}

export interface ListenersResponse {
  listeners: ListenerInfo[];
  available_models?: string[];
}

export async function fetchListeners(): Promise<ListenersResponse> {
  return requestJSON<ListenersResponse>("/api/listeners");
}

export async function updateListeners(
  listeners: ListenerInfo[],
): Promise<{ status: string; reloading: boolean }> {
  return requestJSON<{ status: string; reloading: boolean }>("/api/listeners", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      listeners: listeners.map((l) => ({ name: l.name, listen: l.listen, models: l.models })),
    }),
  });
}

// globMatches converts shell-style globs (* and ?) for client-side match
// previews. The server remains the source of truth.
export function globMatches(patterns: string[], name: string): boolean {
  for (const raw of patterns) {
    const pattern = raw.trim();
    if (!pattern) continue;
    const escaped = pattern.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*").replace(/\?/g, ".");
    try {
      if (new RegExp(`^${escaped}$`).test(name)) return true;
    } catch {
      /* invalid pattern: skip */
    }
  }
  return false;
}
