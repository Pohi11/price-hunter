/** Runtime configuration, written to /config.json by Terraform per environment. */
export type AppConfig = {
  apiBaseUrl: string;
  authority: string; // Cognito user pool issuer
  clientId: string;
  cognitoDomain: string; // managed login domain, used for logout
  environment: string;
};

/** Returns null when no config is deployed (local dev: same-origin API, no auth). */
export async function loadConfig(): Promise<AppConfig | null> {
  try {
    const res = await fetch("/config.json", { cache: "no-store" });
    if (!res.ok || !res.headers.get("content-type")?.includes("json")) return null;
    const cfg = (await res.json()) as AppConfig;
    return cfg.clientId ? cfg : null;
  } catch {
    return null;
  }
}
