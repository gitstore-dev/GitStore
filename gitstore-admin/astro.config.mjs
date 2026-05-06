// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

import { defineConfig } from 'astro/config';
import react from '@astrojs/react';

// https://astro.build/config
export default defineConfig({
  integrations: [react()],
  output: 'static',
  server: {
    port: 3000,
    host: true
  },
  vite: {
    server: {
      proxy: {
        '/graphql': {
          target: process.env.GITSTORE_GRAPHQL_URL || 'http://localhost:4000',
          changeOrigin: true
        },
        '/api': {
          target: process.env.GITSTORE_GRAPHQL_URL || 'http://localhost:4000',
          changeOrigin: true
        }
      }
    }
  }
});
