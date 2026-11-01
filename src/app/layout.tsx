import type { Metadata } from "next";
import { Suspense } from "react";
import "./globals.css";
export const metadata: Metadata = {
  title: "Sign Restaurant",
  description: "Local staff workspace for restaurant catalog and availability",
};
export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body>
        <Suspense fallback={<output>Loading…</output>}>{children}</Suspense>
      </body>
    </html>
  );
}
