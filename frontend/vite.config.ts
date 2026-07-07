import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";
import path from "path";
// https://vite.dev/config/
export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  server: {
    // Must match frontend:dev:serverUrl in wails.json. strictPort makes a
    // port collision fail loudly instead of Vite drifting to another port
    // while Wails keeps loading whatever app owns this one.
    port: 5183,
    strictPort: true,
  },
  resolve: {
    alias: {
      $lib: path.resolve("./src/lib"),
    },
  },
});
