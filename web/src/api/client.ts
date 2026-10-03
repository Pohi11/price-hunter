import type { components } from "./schema";

export type Product = components["schemas"]["Product"];
export type Money = components["schemas"]["Money"];
export type PriceHistory = components["schemas"]["PriceHistory"];
export type Check = components["schemas"]["Check"];
export type Profile = components["schemas"]["Profile"];
export type Problem = components["schemas"]["Problem"];
export type CreateProductRequest = components["schemas"]["CreateProductRequest"];
export type UpdateProductRequest = components["schemas"]["UpdateProductRequest"];

/** An API error carrying the RFC 9457 problem body. */
export class ApiError extends Error {
  constructor(
    public status: number,
    public problem: Problem,
    public retryAfter?: number,
  ) {
    super(problem.detail || problem.title);
  }

  /** Field-level validation messages keyed by field name. */
  fieldErrors(): Record<string, string> {
    const out: Record<string, string> = {};
    for (const e of this.problem.errors ?? []) out[e.field] = e.message;
    return out;
  }
}

type TokenProvider = () => Promise<string | null> | string | null;
let getToken: TokenProvider = () => null;

/** Installs the auth token source (Cognito in AWS; none locally). */
export function setTokenProvider(p: TokenProvider) {
  getToken = p;
}

let base = "";

/** Points the client at the API (from runtime config; same-origin when unset). */
export function setApiBase(url: string) {
  base = url.replace(/\/$/, "");
}

async function request<T>(method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<T> {
  const token = await getToken();
  const res = await fetch(base + path, {
    method,
    headers: {
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...headers,
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  const data = text ? JSON.parse(text) : undefined;
  if (!res.ok) {
    const problem: Problem = data ?? { type: "about:blank", title: res.statusText, status: res.status };
    const ra = res.headers.get("Retry-After");
    throw new ApiError(res.status, problem, ra ? Number(ra) : undefined);
  }
  return data as T;
}

export const api = {
  listProducts: () => request<{ items: Product[]; next_cursor?: string }>("GET", "/v1/products?limit=100"),
  getProduct: (id: string) => request<Product>("GET", `/v1/products/${encodeURIComponent(id)}`),
  createProduct: (req: CreateProductRequest) => request<Product>("POST", "/v1/products", req),
  updateProduct: (id: string, req: UpdateProductRequest, version?: number) =>
    request<Product>("PATCH", `/v1/products/${encodeURIComponent(id)}`, req, version ? { "If-Match": `W/"${version}"` } : {}),
  deleteProduct: (id: string) => request<void>("DELETE", `/v1/products/${encodeURIComponent(id)}`),
  prices: (id: string, from: Date, maxPoints = 400) =>
    request<PriceHistory>(
      "GET",
      `/v1/products/${encodeURIComponent(id)}/prices?from=${encodeURIComponent(from.toISOString().replace(/\.\d{3}Z$/, "Z"))}&max_points=${maxPoints}`,
    ),
  triggerCheck: (id: string) => request<Check>("POST", `/v1/products/${encodeURIComponent(id)}/checks`),
  getCheck: (id: string, checkId: string) =>
    request<Check>("GET", `/v1/products/${encodeURIComponent(id)}/checks/${encodeURIComponent(checkId)}`),
  listChecks: (id: string) => request<{ items: Check[] }>("GET", `/v1/products/${encodeURIComponent(id)}/checks?limit=15`),
  me: () => request<Profile>("GET", "/v1/me"),
  updateMe: (notifications_enabled: boolean) => request<Profile>("PATCH", "/v1/me", { notifications_enabled }),
  retailers: () =>
    request<{ items: { id: string; name: string; domains: string[]; currency?: string }[]; generic_allowed: boolean }>(
      "GET",
      "/v1/retailers",
    ),
};
