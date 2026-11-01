import Link from "next/link";
import { signOut } from "../sign-in/actions";
export default function AccessDenied() {
  return (
    <main className="access-panel">
      <p className="brand">Service desk</p>
      <p className="eyebrow">Local staff workspace</p>
      <h1>Staff access is not available.</h1>
      <p>Your account does not currently have staff access.</p>
      <Link href="/sign-in">Return to sign in</Link>
      <form action={signOut}>
        <button type="submit">Sign out</button>
      </form>
    </main>
  );
}
