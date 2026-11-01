"use client";

import { useState } from "react";
import { useFormStatus } from "react-dom";

export function SignInFields() {
  const [visible, setVisible] = useState(false);
  const { pending } = useFormStatus();
  return (
    <div className="host-signin-fields" aria-busy={pending}>
      <div className="host-signin-field">
        <label htmlFor="email">Work email</label>
        <input
          id="email"
          name="email"
          type="email"
          autoComplete="username"
          required
          maxLength={254}
          readOnly={pending}
        />
      </div>
      <div className="host-signin-field">
        <label htmlFor="password">Password</label>
        <div className="host-signin-password-field">
          <input
            id="password"
            name="password"
            type={visible ? "text" : "password"}
            autoComplete="current-password"
            required
            maxLength={128}
            readOnly={pending}
          />
          <button
            className="host-signin-password-toggle"
            type="button"
            aria-label={visible ? "Hide password" : "Show password"}
            aria-controls="password"
            aria-pressed={visible}
            disabled={pending}
            onClick={() => setVisible(!visible)}
          >
            <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
              <path
                d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12Z"
                stroke="currentColor"
                strokeWidth="1.5"
              />
              <circle
                cx="12"
                cy="12"
                r="3"
                stroke="currentColor"
                strokeWidth="1.5"
              />
            </svg>
          </button>
        </div>
      </div>
      <button className="host-signin-submit" type="submit" disabled={pending}>
        <span>{pending ? "Signing in…" : "Sign in"}</span>
        <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <path
            d="M5 12h14m-5-5 5 5-5 5"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      </button>
    </div>
  );
}
