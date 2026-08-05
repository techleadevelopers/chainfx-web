import { resolve } from 'path';
import { defineConfig } from 'vite';

export default defineConfig({
  server: {
    host: '0.0.0.0',
    allowedHosts: true,
    proxy: {
      '/api/public/news': {
        target: 'https://api-production-bc748.up.railway.app',
        changeOrigin: true,
      },
      '/proxy/rss/coindesk': {
        target: 'https://www.coindesk.com',
        changeOrigin: true,
        rewrite: () => '/arc/outboundfeeds/rss/',
      },
      '/proxy/rss/cointelegraph': {
        target: 'https://cointelegraph.com',
        changeOrigin: true,
        rewrite: () => '/rss',
      },
      '/proxy/rss/decrypt': {
        target: 'https://decrypt.co',
        changeOrigin: true,
        rewrite: () => '/feed',
      },
    },
  },
  build: {
    outDir: 'dist',
    rollupOptions: {
      input: {
        index: resolve(__dirname, 'index.html'),
        admin: resolve(__dirname, 'admin.html'),
        agentConsole: resolve(__dirname, 'app/agent/index.html'),
        developerConsole: resolve(__dirname, 'app/developer/index.html')
      }
    }
  }
});
