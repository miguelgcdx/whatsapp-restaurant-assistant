import { signIn } from "./actions";
export default async function SignIn({
  searchParams,
}: {
  searchParams: Promise<{ error?: string }>;
}) {
  const params = await searchParams;
  return (
    <main>
      <h1>Staff sign in</h1>
      <p>Use your generated local staff credentials.</p>
      {params.error && <p role="alert">Sign-in could not be completed.</p>}
      <form action={signIn}>
        <label htmlFor="email">Email</label>
        <input
          id="email"
          name="email"
          type="email"
          autoComplete="username"
          required
          maxLength={254}
        />
        <label htmlFor="password">Password</label>
        <input
          id="password"
          name="password"
          type="password"
          autoComplete="current-password"
          required
          maxLength={128}
        />
        <button type="submit">Sign in</button>
      </form>
    </main>
  );
}
