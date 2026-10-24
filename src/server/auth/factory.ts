import "server-only";
import { betterAuth } from "better-auth";
import { nextCookies } from "better-auth/next-js";
import { jwt } from "better-auth/plugins/jwt";
import { Kysely, PostgresDialect } from "kysely";
import { Pool } from "pg";
import { type DatabaseTarget, poolOptions } from "./config.ts";

export function createAuth(config: {
  target: DatabaseTarget;
  origin: string;
  secret: string;
  setup?: boolean;
  next?: boolean;
  sessionExpiresIn?: number;
}) {
  const pool = new Pool(poolOptions(config.target));
  // Driver errors may contain sensitive configuration. Request errors fail closed.
  pool.on("error", () => {});
  const auth = betterAuth({
    database: {
      db: new Kysely({ dialect: new PostgresDialect({ pool }) }),
      type: "postgres",
      schemaName: "auth",
    },
    baseURL: config.origin,
    secret: config.secret,
    telemetry: { enabled: false },
    logger: { disabled: true },
    trustedOrigins: [config.origin],
    emailAndPassword: { enabled: true, disableSignUp: !config.setup },
    session: {
      cookieCache: { enabled: false },
      expiresIn: config.sessionExpiresIn ?? 60 * 60 * 8,
    },
    disabledPaths: ["/token"],
    plugins: [
      jwt({
        disableSettingJwtHeader: true,
        jwt: {
          issuer: config.origin,
          audience: "restaurant-assistant-api",
          expirationTime: "60s",
          definePayload: () => ({}),
        },
      }),
      ...(config.next ? [nextCookies()] : []),
    ],
  });
  return { auth, pool };
}
export type StaffAuth = ReturnType<typeof createAuth>["auth"];
