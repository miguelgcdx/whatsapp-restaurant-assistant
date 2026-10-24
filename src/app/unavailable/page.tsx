import Link from "next/link";
export default function Unavailable() {
  return (
    <main>
      <h1>Staff access is temporarily unavailable.</h1>
      <p>Please try again when the local services are available.</p>
      <Link href="/dashboard">Try again</Link>
    </main>
  );
}
