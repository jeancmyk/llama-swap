export interface ModelConfigInfo {
  model_id: string;
  source_file?: string;
  editable: boolean;
  reason?: string;
  yaml: string;
}

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
      const body = (await response.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      /* keep status code */
    }
    throw new Error(message);
  }
  return (await response.json()) as T;
}

export async function fetchModelConfig(modelId: string): Promise<ModelConfigInfo> {
  return requestJSON<ModelConfigInfo>(`/api/models/config/${encodeURIComponent(modelId)}`);
}

export async function updateModelConfig(modelId: string, yaml: string): Promise<{ status: string; reloading: boolean }> {
  return requestJSON<{ status: string; reloading: boolean }>(
    `/api/models/config/${encodeURIComponent(modelId)}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ yaml }),
    },
  );
}

export async function fetchListeners(): Promise<ListenerInfo[]> {
  const state = await requestJSON<{ listeners: ListenerInfo[] }>("/api/listeners");
  return state.listeners ?? [];
}

export async function updateListeners(listeners: ListenerInfo[]): Promise<{ status: string; reloading: boolean }> {
  return requestJSON<{ status: string; reloading: boolean }>("/api/listeners", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      listeners: listeners.map((l) => ({ name: l.name, listen: l.listen, models: l.models })),
    }),
  });
}

// globToRegExp converts a shell-style glob (* and ?) to a RegExp for
// client-side match previews. Server remains the source of truth.
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
