// Copyright 2026 Geda
// SPDX-License-Identifier: Apache-2.0

import { defineConfig } from "vite";

// The whole UI is one page with no routing, so there is nothing to split and
// nothing to lazy-load. Inlining keeps the built app a single request against
// the embedded asset server.
export default defineConfig({
  build: {
    target: "es2022",
    assetsInlineLimit: 1024 * 1024,
    chunkSizeWarningLimit: 1024,
  },
});
