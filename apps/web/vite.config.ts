import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig } from "vite"

const api = process.env.SLOPPA_API_URL ?? "http://127.0.0.1:4100"

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    host: "127.0.0.1",
    port: 5173,
    proxy: { "/api": api, "/f": api, "/mcp": api }
  }
})
