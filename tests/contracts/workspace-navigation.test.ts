import { existsSync } from "node:fs";
import Link from "next/link";
import {
  Children,
  isValidElement,
  type ReactElement,
  type ReactNode,
} from "react";
import { describe, expect, it } from "vitest";
import CatalogError from "../../src/app/dashboard/catalog/error";
import { StaffFrame } from "../../src/app/dashboard/staff-frame";

type ElementProps = {
  children?: ReactNode;
  href?: string;
  prefetch?: boolean;
  "aria-current"?: string;
  id?: string;
  tabIndex?: number;
};

function elements(node: ReactNode): ReactElement<ElementProps>[] {
  const result: ReactElement<ElementProps>[] = [];
  for (const child of Children.toArray(node)) {
    if (isValidElement<ElementProps>(child)) {
      result.push(child, ...elements(child.props.children));
    }
  }
  return result;
}

// Structural component contracts only; not browser/no-flash acceptance.
describe("workspace navigation wiring", () => {
  it("does not replace Catalog with a route loading fallback", () => {
    const loadingModules = ["tsx", "ts", "jsx", "js"].filter((extension) =>
      existsSync(
        new URL(
          `../../src/app/dashboard/catalog/loading.${extension}`,
          import.meta.url,
        ),
      ),
    );
    expect(loadingModules).toEqual([]);
  });

  it.each([
    "overview",
    "catalog",
    "orders",
  ] as const)("%s sidebar uses client navigation in both directions without prefetch", (section) => {
    const links = elements(StaffFrame({ section, children: null })).filter(
      (element) => element.props.href?.startsWith("/dashboard"),
    );
    expect(links.map((link) => link.props.href)).toEqual([
      "/dashboard",
      "/dashboard/catalog",
      "/dashboard/orders",
    ]);
    for (const link of links) {
      expect(link.type).toBe(Link);
      expect(link.props.prefetch).toBe(false);
    }
    expect(links.map((link) => link.props["aria-current"])).toEqual(
      ["overview", "catalog", "orders"].map((entry) =>
        entry === section ? "page" : undefined,
      ),
    );
  });

  it("preserves the native skip link and its focusable content target", () => {
    const tree = elements(StaffFrame({ section: "overview", children: null }));
    const skip = tree.find((element) => element.props.href === "#main-content");
    expect(skip?.type).toBe("a");
    expect(skip?.props.children).toBe("Skip to content");
    expect(tree.filter((element) => element.type === "main")).toHaveLength(1);
    const main = tree.find((element) => element.props.id === "main-content");
    expect(main?.type).toBe("main");
    expect(main?.props.tabIndex).toBe(-1);
  });

  it("preserves native same-URL catalog error recovery", () => {
    const recovery = elements(CatalogError()).filter(
      (element) => element.props.children === "Start again",
    );
    expect(recovery).toHaveLength(1);
    expect(recovery[0]?.type).toBe("a");
    expect(recovery[0]?.props.href).toBe("/dashboard/catalog");
  });

  it("preserves error link targets and client navigation back to dashboard", () => {
    const links = elements(CatalogError()).filter(
      (element) => element.props.href,
    );
    expect(links.map((link) => link.props.href)).toEqual([
      "/dashboard/catalog",
      "/dashboard",
    ]);
    expect(links.map((link) => link.props.children)).toEqual([
      "Start again",
      "Back to dashboard",
    ]);
    const back = links.find((link) => link.props.href === "/dashboard");
    expect(back?.type).toBe(Link);
    expect(back?.props.prefetch).toBe(false);
  });
});
