// Model IDs are config keys, URL path segments and the /v1/models id, so the
// server (validModelID in internal/server/configedit.go) restricts them to
// URL-safe characters. The UI is slightly stricter and requires the ID to
// start with a letter or digit.
const MODEL_ID_PATTERN = /^[a-zA-Z0-9][a-zA-Z0-9._:-]*$/;

export function isValidModelID(id: string): boolean {
  return id.length > 0 && id.length <= 128 && MODEL_ID_PATTERN.test(id);
}
