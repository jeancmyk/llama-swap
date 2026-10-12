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

// SaveOptions controls the save/apply split. The default (reload: false) only
// saves the change to the config file; the running server keeps the old
// settings until a separate Apply (POST /api/reload) hot-reloads.
export interface SaveOptions {
  reload?: boolean;
}

export async function updateModelConfig(
  modelId: string,
  yaml: string,
  options?: SaveOptions,
): Promise<{ status: string; reloading: boolean }> {
  return requestJSON<{ status: string; reloading: boolean }>(
    `/api/models/config/${encodeURIComponent(modelId)}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ yaml, reload: options?.reload ?? false }),
    },
  );
}

export async function renameModel(
  modelId: string,
  name: string,
  options?: SaveOptions,
): Promise<{ status: string; reloading: boolean }> {
  return requestJSON<{ status: string; reloading: boolean }>(
    `/api/models/config/${encodeURIComponent(modelId)}`,
    {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, reload: options?.reload ?? false }),
    },
  );
}

// renameModelId renames the model's ID (the models: key), which is the model's
// identity everywhere: its URL, the /v1/models id and the UI links. The
// server rewrites exact references (listeners, groups, selectors, profiles,
// preloads, matrix) and hot-reloads, so a running model restarts under the
// new ID.
export async function renameModelId(
  modelId: string,
  newId: string,
): Promise<{ status: string; new_id: string; reloading: boolean }> {
  return requestJSON<{ status: string; new_id: string; reloading: boolean }>(
    `/api/models/rename/${encodeURIComponent(modelId)}`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ new_id: newId }),
    },
  );
}

export async function addModelConfig(
  modelId: string,
  yaml: string,
  options?: SaveOptions,
): Promise<{ status: string; reloading: boolean }> {
  return requestJSON<{ status: string; reloading: boolean }>(
    `/api/models/config/${encodeURIComponent(modelId)}`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ yaml, reload: options?.reload ?? false }),
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

// capabilitiesURL builds /api/models/capabilities/{model}/accept|discard.
// Model ids may contain "/", so the id is segment-encoded while the action
// stays the final path segment (the server splits it off first).
function capabilitiesURL(modelId: string, action: "accept" | "discard"): string {
  const segments = modelId.split("/").map(encodeURIComponent).join("/");
  return `/api/models/capabilities/${segments}/${action}`;
}

// acceptModelCapabilities adopts the auto-filled capability fields: the
// pending marker is stripped and the values become plain config.
export async function acceptModelCapabilities(
  modelId: string,
): Promise<{ status: string; reloading: boolean }> {
  return requestJSON<{ status: string; reloading: boolean }>(capabilitiesURL(modelId, "accept"), {
    method: "POST",
  });
}

// discardModelCapabilities removes the auto-filled fields from the config,
// stops the values being advertised at once, and prevents re-adding.
export async function discardModelCapabilities(
  modelId: string,
): Promise<{ status: string; reloading: boolean }> {
  return requestJSON<{ status: string; reloading: boolean }>(capabilitiesURL(modelId, "discard"), {
    method: "POST",
  });
}

// triggerReload is the explicit Apply step of the save/apply split: it hot-
// reloads the saved config, which stops running models.
export async function triggerReload(): Promise<{ status: string; reloading: boolean }> {
  return requestJSON<{ status: string; reloading: boolean }>("/api/reload", { method: "POST" });
}

// fetchConfigStatus reports whether the config file is ahead of the running
// server (saved changes pending an apply).
export async function fetchConfigStatus(): Promise<{ dirty: boolean }> {
  return requestJSON<{ dirty: boolean }>("/api/config/status");
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
