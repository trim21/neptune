import {defineConfig} from 'tsdown';

export default defineConfig({
  entry: ['src/client.browser.ts', 'src/client.node.ts'],
  format: ['esm'],
  dts: true,
  clean: true,
  sourcemap: true,
  outExtensions: () => ({js: '.js', dts: '.d.ts'}),
});
