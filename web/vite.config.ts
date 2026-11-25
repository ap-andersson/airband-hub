import { defineConfig } from "vite";
import preact from "@preact/preset-vite";

// `npm run dev` serves the UI with hot reload and forwards everything else to a running hub.
// Point it at one with HUB_URL, e.g. HUB_URL=http://raspberrypi.local:8090 npm run dev
const hub = process.env.HUB_URL ?? "http://localhost:8000";
// To open the dev server from a phone by this machine's name, list the name(s), comma separated:
// HUB_DEV_HOSTS=mypc.local npm run dev -- --host  (Vite rejects unknown host names otherwise)
const allowedHosts = (process.env.HUB_DEV_HOSTS ?? "").split(",").filter(Boolean);

export default defineConfig({
  plugins: [preact()],
  build: { outDir: "dist", emptyOutDir: true },
  server: {
    allowedHosts,
    proxy: {
      "/api": { target: hub, changeOrigin: true },
      "^/[^/]+\\.(mp3|ogg|aac)$": { target: hub, changeOrigin: true },
    },
  },
});
