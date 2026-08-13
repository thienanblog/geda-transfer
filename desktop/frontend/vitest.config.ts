// Copyright 2026 Geda
// SPDX-License-Identifier: Apache-2.0

import { defineConfig } from "vitest/config";

// jsdom rather than a real browser: every assertion here is about structure
// and wording -- what the window says and what it asks the Go side to do --
// and none of it depends on layout or on a rendering engine. A browser would
// buy nothing and cost a download in CI.
export default defineConfig({
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.ts"],
    restoreMocks: true,
  },
});
