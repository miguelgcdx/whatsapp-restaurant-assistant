import "server-only";
import type { PoolConfig } from "pg";

export const configurationError = () =>
  new Error("Local authentication configuration is unavailable.");

export function localOrigin(value: string | undefined): string {
  try {
    if (!value) throw configurationError();
    const url = new URL(value);
    if (
      url.protocol !== "http:" ||
      !["127.0.0.1", "[::1]"].includes(url.hostname) ||
      !url.port ||
      Number(url.port) < 1 ||
      url.origin !== value
    )
      throw configurationError();
    return value;
  } catch {
    throw configurationError();
  }
}

export function databaseTarget(value: string | undefined) {
  try {
    if (!value) throw configurationError();
    const url = new URL(value);
    if (
      !["postgres:", "postgresql:"].includes(url.protocol) ||
      !["127.0.0.1", "[::1]"].includes(url.hostname) ||
      !url.port ||
      Number(url.port) < 1 ||
      !url.username ||
      url.hash ||
      !/^\/restaurant_foundation_[a-zA-Z0-9_]+$/.test(url.pathname)
    )
      throw configurationError();
    const entries = [...url.searchParams.entries()];
    if (
      entries.length > 1 ||
      entries.some(([key, value]) => key !== "sslmode" || value !== "disable")
    )
      throw configurationError();
    return {
      host: url.hostname === "[::1]" ? "::1" : url.hostname,
      port: Number(url.port),
      database: url.pathname.slice(1),
      user: decodeURIComponent(url.username),
      password: decodeURIComponent(url.password),
      ssl: false as const,
    };
  } catch {
    throw configurationError();
  }
}

export type DatabaseTarget = ReturnType<typeof databaseTarget>;
export function poolOptions(target: DatabaseTarget): PoolConfig {
  // Explicit targets/security fields: never inherit driver target/password defaults.
  return {
    ...target,
    password: async () => target.password,
    sslnegotiation: "postgres",
    application_name: "local-staff-auth",
    options: "-c search_path=auth",
    max: 4,
    connectionTimeoutMillis: 2000,
    idleTimeoutMillis: 1000,
    allowExitOnIdle: true,
  };
}

export function originAllowed(
  headers: Headers,
  origin: string,
  write: boolean,
): boolean {
  const supplied = headers.get("origin");
  return supplied === origin || (!write && supplied === null);
}
export function targetIdentity(target: DatabaseTarget) {
  return {
    host: target.host,
    port: target.port,
    database: target.database,
    user: target.user,
  };
}
