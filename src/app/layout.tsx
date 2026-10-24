import type { Metadata } from "next";
import { Suspense } from "react";
import "./globals.css";
export const metadata: Metadata = {
  title: "Local staff access",
  description: "Local restaurant staff access foundation",
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
