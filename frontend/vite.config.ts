import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const proxyTarget = process.env.SLIDESMITH_API_PROXY || "http://10.2.37.236:18080";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: proxyTarget,
        changeOrigin: true,
      },
      "/healthz": {
        target: proxyTarget,
        changeOrigin: true,
      },
    },
  },
});

