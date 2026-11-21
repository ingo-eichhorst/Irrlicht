// main.js imports Herdr's runtime modules by bare name. Herdr resolves them
// itself; here they are the recording stand-ins in the mod's test/stubs, so
// test/main.render.test.js can draw the panel under node. The lib/ modules
// import nothing from Herdr, so this changes nothing for their tests.
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vitest/config';

const stub = (name) => fileURLToPath(new URL(`./mods/irrlicht/test/stubs/${name}.js`, import.meta.url));

export default defineConfig({
  resolve: {
    alias: { 'gpui-kit': stub('gpui-kit'), 'gpui-base': stub('gpui-base'), herdr: stub('herdr') },
  },
});
