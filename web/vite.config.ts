import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The build is embedded into the Go binary (internal/web serves dist/).
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "../internal/web/dist",
    emptyOutDir: false, // keep the tracked placeholder.html
    sourcemap: false,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes("/node_modules/")) return undefined;
          if (/\/node_modules\/(recharts|d3-[^/]+|victory-vendor)\//.test(id)) return "charts";
          if (/\/node_modules\/(react|react-dom|react-router|react-router-dom|scheduler|@tanstack\/react-query)\//.test(id)) return "vendor";
          return undefined;
        },
      },
    },
  },
  server: {
    proxy: {
      "/api": { target: process.env.ATLANTIS_API ?? "http://127.0.0.1:8080", changeOrigin: false },
      "/metrics": { target: process.env.ATLANTIS_API ?? "http://127.0.0.1:8080" },
    },
  },
  test: { environment: "jsdom", include: ["src/**/*.test.{ts,tsx}"] },
});
