import type { Metadata, Viewport } from "next";
import type { ReactNode } from "react";

import "./globals.css";

/**
 * EXORYN is the commercial name; the technical names underneath it are unchanged.
 *
 * The brand authority is explicit that a commercial identity does not rename services,
 * packages, database objects, APIs or event names, and none of those moved. What changed
 * is what a customer reads at the top of a browser tab.
 *
 * The descriptor is the approved one, and the disclaimer stays. This product governs
 * order flow; it does not advise on it, and a console that let anyone forget that would
 * be the first step toward a claim nobody may make.
 */
export const metadata: Metadata = {
  title: "EXORYN Console",
  description:
    "Assurance infrastructure for autonomous finance. Read-only operator console. " +
    "Not an investment product.",
  icons: { icon: "/favicon.svg" },
};

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
