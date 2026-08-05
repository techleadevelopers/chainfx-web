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
        headers: { 'User-Agent': 'Mozilla/5.0 (compatible; RSSReader/1.0)' },
        rewrite: () => '/arc/outboundfeeds/rss/',
      },
      '/proxy/rss/cointelegraph': {
        target: 'https://cointelegraph.com',
        changeOrigin: true,
        headers: { 'User-Agent': 'Mozilla/5.0 (compatible; RSSReader/1.0)' },
        rewrite: () => '/rss',
      },
      '/proxy/rss/decrypt': {
        target: 'https://decrypt.co',
        changeOrigin: true,
        headers: { 'User-Agent': 'Mozilla/5.0 (compatible; RSSReader/1.0)' },
        rewrite: () => '/feed',
      },
      '/proxy/rss/theblock': {
        target: 'https://www.theblock.co',
        changeOrigin: true,
        headers: { 'User-Agent': 'Mozilla/5.0 (compatible; RSSReader/1.0)' },
        rewrite: () => '/rss.xml',
      },
      '/proxy/rss/beincrypto': {
        target: 'https://beincrypto.com',
        changeOrigin: true,
        headers: { 'User-Agent': 'Mozilla/5.0 (compatible; RSSReader/1.0)' },
        rewrite: () => '/feed/',
      },
      '/proxy/rss/newsbtc': {
        target: 'https://www.newsbtc.com',
        changeOrigin: true,
        headers: { 'User-Agent': 'Mozilla/5.0 (compatible; RSSReader/1.0)' },
        rewrite: () => '/feed/',
      },
      '/proxy/rss/utoday': {
        target: 'https://u.today',
        changeOrigin: true,
        headers: { 'User-Agent': 'Mozilla/5.0 (compatible; RSSReader/1.0)' },
        rewrite: () => '/rss',
      },
      '/proxy/rss/cryptoslate': {
        target: 'https://cryptoslate.com',
        changeOrigin: true,
        headers: { 'User-Agent': 'Mozilla/5.0 (compatible; RSSReader/1.0)' },
        rewrite: () => '/feed/',
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
