import { defineConfig } from "vite";
import solid from "vite-plugin-solid";
export default defineConfig({
  plugins: [solid()],
  // The server embeds this directory (internal/webui), so it must stay inside
  // that Go package.
  build: { outDir: "internal/webui/dist", emptyOutDir: true },
  server: {
    host: "127.0.0.1",
    port: 6201,
    allowedHosts: ["sorry-portainer.home.serial-experiments.com"],
    proxy: {
      "/api": {
        target: "http://127.0.0.1:6200",
        changeOrigin: false,
      },
    },
  },
});
