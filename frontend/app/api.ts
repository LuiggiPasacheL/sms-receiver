export class APIError extends Error {
  constructor(message: string, readonly status: number) { super(message); }
}

export async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await fetch(path, { ...options, credentials: "same-origin", cache: "no-store" });
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    throw new APIError(body.error || "Unable to reach the inbox. Please try again.", response.status);
  }
  if (response.status === 204) return undefined as T;
  return response.json();
}
