import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  reactStrictMode: true,

  // The development overlay is off so that what a developer sees locally is what the
  // Console actually renders. It affects `next dev` only and changes nothing about the
  // production build; the reason it is here is that the documentation screenshots are
  // taken from a real local run, and a floating framework badge in one of them would be
  // a detail of the toolchain presented as a detail of the product.
  devIndicators: false,
};

export default nextConfig;
