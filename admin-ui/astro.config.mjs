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
    resolve: {
      alias: {
        '@apollo/client': '@apollo/client/index.js'
      }
    },
    optimizeDeps: {
      include: ['@apollo/client'],
      esbuildOptions: {
        target: 'es2020'
      }
    },
    ssr: {
      external: ['@apollo/client', 'react', 'react-dom']
    },
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
