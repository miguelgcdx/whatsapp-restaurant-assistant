"use client";

export default function CatalogError() {
  return (
    <main>
      <h1>Staff catalog</h1>
      <p role="alert">Catalog is temporarily unavailable. Try again later.</p>
      <a href="/dashboard/catalog">Start again</a>
      <p>
        <a href="/dashboard">Back to dashboard</a>
      </p>
    </main>
  );
}
