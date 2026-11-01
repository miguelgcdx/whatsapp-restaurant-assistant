"use client";

import Link from "next/link";
import { StaffFrame } from "../staff-frame";

export default function CatalogError() {
  return (
    <StaffFrame section="catalog">
      <h1>Staff catalog</h1>
      <p role="alert">Catalog is temporarily unavailable. Try again later.</p>
      <a href="/dashboard/catalog">Start again</a>
      <p>
        <Link href="/dashboard" prefetch={false}>
          Back to dashboard
        </Link>
      </p>
    </StaffFrame>
  );
}
