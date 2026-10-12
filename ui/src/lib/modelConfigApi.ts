export interface ModelFileInfo {
  flag: string;
  path: string;
}

export interface ModelConfigInfo {
  model_id: string;
  source_file?: string;
  editable: boolean;
  reason?: string;
  yaml: string;
  backend_path?: string;
  model_files?: ModelFileInfo[];
}

export interface BackendInfo {
  path: string;
  models: string[];
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

export async function renameModel(
  modelId: string,
  name: string,
): Promise<{ status: string; reloading: boolean }> {
  return requestJSON<{ status: string; reloading: boolean }>(
    `/api/models/config/${encodeURIComponent(modelId)}`,
    {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    },
  );
}

export async function addModelConfig(
  modelId: string,
  yaml: string,
): Promise<{ status: string; reloading: boolean }> {
  return requestJSON<{ status: string; reloading: boolean }>(
    `/api/models/config/${encodeURIComponent(modelId)}`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ yaml }),
    },
  );
}

export async function removeModelConfig(
  modelId: string,
): Promise<{ status: string; reloading: boolean; listeners_pruned: boolean }> {
  return requestJSON<{ status: string; reloading: boolean; listeners_pruned: boolean }>(
    `/api/models/config/${encodeURIComponent(modelId)}`,
    { method: "DELETE" },
  );
}

export async function fetchBackends(): Promise<BackendInfo[]> {
  const state = await requestJSON<{ backends: BackendInfo[] }>("/api/backends");
  return state.backends ?? [];
}

export interface ListenersResponse {
  listeners: ListenerInfo[];
  available_models?: string[];
}

export async function fetchListeners(): Promise<ListenersResponse> {
  return requestJSON<ListenersResponse>("/api/listeners");
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
