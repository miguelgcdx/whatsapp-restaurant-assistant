import type { ReactNode } from "react";
import { StaffFrame } from "../staff-frame";
import "./orders.css";

export default function OrdersLayout({ children }: { children: ReactNode }) {
  return <StaffFrame section="orders">{children}</StaffFrame>;
}
