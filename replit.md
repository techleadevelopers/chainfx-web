# Swappy (ChainFX) — Project Overview

Swappy is a crypto/PIX on-off ramp platform: a static/Vite HTML+JS frontend (index.html, admin.html) paired with an Express backend design described in README.md (API, on-chain worker, PIX payout worker, price-cache worker). Also includes `signer/`, `bsc-signer/` (key-isolated signing services) and Hardhat contracts config.

## Current setup status
- **Frontend only** is running on Replit via the `Start application` workflow: `npm run dev -- --port 5000` (Vite, port 5000, `host: 0.0.0.0` / `allowedHosts: true` already configured in `vite.config.js`).
- The backend (`server/server.js` referenced in `package.json`'s `start` script) does **not exist yet** — the `server/` folder is empty. `npm start` will fail until it's implemented.
- The frontend currently calls a live external API at `stablecoin-payment-gateway-production-3ee2.up.railway.app` for prices/rates (see `main.js`); this is blocked by CORS in this environment, but the UI has a fallback (CoinGecko-sourced rates) and still renders/functions.
- `signer/` and `bsc-signer/` are empty stub directories for on-chain signing services; not configured or run here.

## Running the project
- `npm run dev -- --port 5000` — starts the Vite dev server (bound to the `Start application` workflow).
- `npm start` — would run the Express backend, but it's not implemented yet (see above).

## User preferences
- (none recorded yet)
