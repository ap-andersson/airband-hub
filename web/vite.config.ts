import { defineConfig } from "vite";
import preact from "@preact/preset-vite";

// `npm run dev` serves the UI with hot reload and forwards everything else to a running hub.
// Point it at one with HUB_URL, e.g. HUB_URL=http://raspberrypi.local:8090 npm run dev
const hub = process.env.HUB_URL ?? "http://localhost:8000";

export default defineConfig({
  plugins: [preact()],
  build: { outDir: "dist", emptyOutDir: true },
  server: {
    proxy: {
      "/api": { target: hub, changeOrigin: true },
      "^/[^/]+\\.(mp3|ogg|aac)$": { target: hub, changeOrigin: true },
    },
  },
});
