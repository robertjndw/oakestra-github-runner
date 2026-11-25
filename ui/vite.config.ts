import { oakestraAddon } from '@oakestra/dashboard-sdk/vite';
import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

// The dashboard refuses a bundle whose id differs from the name the addons engine reports, and a
// locally registered addon is reported as `local--<name>`. So the id is a build-time setting:
//   ADDON_ID=local--github-runner pnpm build
const addonId = process.env.ADDON_ID ?? 'github-runner';

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    // `prefix` must equal the one in prefix(...) in src/addon.css.
    oakestraAddon({ name: addonId, prefix: 'ghr', port: 5181 }),
  ],
  server: {
    proxy: {
      // The Go backend serves /api itself, so unlike the generated nginx template nothing is
      // stripped. The dashboard dev server forwards /addon-assets/<id>/... here, see README.md.
      '/api': {
        target: process.env.GHR_BACKEND ?? 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
});
