import { defineConfig } from "tsup";

// Dual ESM + CJS build with declaration files. Targets ES2022 so the output
// runs on Node 18+, Cloudflare Workers, and Vercel Edge without polyfills.
export default defineConfig({
  entry: ["src/index.ts"],
  format: ["esm", "cjs"],
  dts: true,
  sourcemap: true,
  clean: true,
  target: "es2022",
  treeshake: true,
  splitting: false,
  outExtension: ({ format }) => ({ js: format === "cjs" ? ".cjs" : ".js" }),
});
