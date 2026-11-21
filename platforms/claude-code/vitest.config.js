import { defineConfig } from 'vitest/config'

// Two test runners share this folder and must never pick up each other's
// files:
//   - vitest runs lib/**/*.test.js, the pure data-shaping modules, in Node
//     (no DOM: the hooks module that imports them has none either);
//   - `claude plugin test .` runs tests/*.test.tsx against the engine, which
//     import 'claude-code/testing' and would fail to load here.
// So vitest's include is narrowed to lib/, rather than left at its default
// glob, which would also match tests/*.test.tsx.
//
// The one jsdom file is lib/quota.parity.test.js, which opts in by its own
// `@vitest-environment jsdom` docblock: it imports the dashboard's
// quotaChips.js, whose import graph wires a DOM at load.
export default defineConfig({
  test: {
    include: ['lib/**/*.test.js'],
    environment: 'node',
  },
})
