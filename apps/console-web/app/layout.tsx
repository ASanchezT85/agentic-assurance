import type { Metadata, Viewport } from "next";
import type { ReactNode } from "react";

export const metadata: Metadata = {
  title: "Agentic Order-Flow Assurance",
  description:
    "Infrastructure for governing AI-generated financial order flow. Not an investment product.",
};

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <body
        style={{
          margin: 0,
          padding: "3rem 1.5rem",
          fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
          lineHeight: 1.6,
        }}
      >
        {children}
      </body>
    </html>
  );
}
