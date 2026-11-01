import { Suspense } from "react";
import { signIn } from "./actions";
import { SignInFields } from "./sign-in-fields";
import "./sign-in.css";

type SignInProps = {
  searchParams: Promise<{ error?: string }>;
};

export default function SignIn({ searchParams }: SignInProps) {
  return (
    <div className="host-signin-page">
      <header className="host-signin-topbar">
        <div className="host-signin-wordmark">
          <svg viewBox="0 0 40 40" fill="none" aria-hidden="true">
            <path
              d="M8 21a12 12 0 0 1 24 0M6 25h28M10 30h20M20 5v3M15 16a6 6 0 0 1 5-3"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
            />
          </svg>
          <span>Service</span>
        </div>
        <p className="host-signin-tagline">
          A little more room for good service.
        </p>
      </header>
      <main className="host-signin-main" aria-labelledby="sign-in-heading">
        <div className="host-signin-content">
          <svg
            className="host-signin-welcome-mark"
            viewBox="0 0 56 56"
            fill="none"
            aria-hidden="true"
          >
            <circle
              cx="28"
              cy="28"
              r="24"
              stroke="currentColor"
              strokeWidth="1.5"
            />
            <circle
              cx="28"
              cy="28"
              r="16"
              stroke="currentColor"
              strokeWidth="1.5"
            />
            <path
              d="M9 13v30M5 13v8c0 5 8 5 8 0v-8m34 0v30m0-30c-5 7-5 15 0 15"
              stroke="currentColor"
              strokeWidth="1.5"
              strokeLinecap="round"
            />
          </svg>
          <header className="host-signin-intro">
            <p className="host-signin-eyebrow">Team access</p>
            <h1 id="sign-in-heading">Good to see you.</h1>
            <p>Sign in to your restaurant workspace.</p>
          </header>
          <Suspense fallback={<output>Loading…</output>}>
            <SignInForm searchParams={searchParams} />
          </Suspense>
          <p className="host-signin-help" id="sign-in-help">
            Need access to your restaurant?
            <br />
            Contact your manager.
          </p>
          <p className="host-signin-access-note">
            <svg viewBox="0 0 20 20" fill="none" aria-hidden="true">
              <rect
                x="4.5"
                y="8"
                width="11"
                height="9"
                rx="1.5"
                stroke="currentColor"
                strokeWidth="1.5"
              />
              <path
                d="M7 8V5a3 3 0 0 1 6 0v3"
                stroke="currentColor"
                strokeWidth="1.5"
              />
            </svg>
            Private access for your restaurant team
          </p>
        </div>
      </main>
      <footer className="host-signin-footer">
        <span>Service · Restaurant workspace</span>
        <span>Menu. Availability. One place.</span>
      </footer>
    </div>
  );
}

async function SignInForm({ searchParams }: SignInProps) {
  const params = await searchParams;
  return (
    <form
      action={signIn}
      className="host-signin-form"
      aria-describedby={
        params.error ? "sign-in-error sign-in-help" : "sign-in-help"
      }
    >
      {params.error && (
        <p id="sign-in-error" role="alert" className="host-signin-feedback">
          We couldn’t sign you in. Check your details and try again.
        </p>
      )}
      <SignInFields />
    </form>
  );
}
