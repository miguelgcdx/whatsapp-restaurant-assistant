import Link from "next/link";
export default function Unavailable() {
  return (
    <main className="access-panel">
      <p className="brand">Service desk</p>
      <p className="eyebrow">Local staff workspace</p>
      <h1>Staff access is temporarily unavailable.</h1>
      <p>Please try again when the local services are available.</p>
      <Link href="/dashboard">Try again</Link>
    </main>
  );
}
