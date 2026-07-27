# ChainFX Node SDK

Minimal Node SDK for the ChainFX Digital FX Payments API.

```js
import { ChainFX } from "@chainfx/sdk";

const chainfx = new ChainFX({
  apiKey: process.env.CHAINFX_API_KEY,
  baseUrl: process.env.CHAINFX_API_BASE_URL || "https://sandbox-api.chainfx.com"
});

const quote = await chainfx.quote({
  side: "buy",
  fiat: "BRL",
  asset: "USDT",
  amount: 500
});

const order = await chainfx.buy({
  fiat: "BRL",
  asset: "USDT",
  amount: 500,
  wallet: "0x000000000000000000000000000000000000dEaD",
  customer: {
    name: "Maria Silva",
    email: "maria@example.com",
    cpf: "12345678909",
    phone: "11999999999",
    birthDate: "1990-05-20",
    address: {
      line1: "Av Paulista",
      number: "1000",
      city: "Sao Paulo",
      state: "SP",
      postalCode: "01310100",
      country: "BR"
    }
  }
});
```

Requires Node 18+ because it uses native `fetch`.

## Current backend coverage

This SDK is intentionally minimal today: quote, buy and sell primitives. The backend now also exposes production endpoints that can be called directly until the SDK grows wrappers:

| Area | Endpoints |
| --- | --- |
| Efi credit-card buy | `POST /api/buy` with `paymentMethod=credit_card`, `paymentToken`, `customer`, `billingAddress` |
| MCP Capability Network | `POST /mcp/initialize`, `POST /mcp/tools/list`, `POST /mcp/tools/call` |
| A2A Agent Pay | `GET /.well-known/agent-card.json`, `POST /a2a`, `POST /a2a/tasks`, `GET /a2a/tasks/{id}`, `GET /a2a/tasks/{id}/events` |
| Agent trust and planning | `GET /.well-known/jwks.json`, `GET /.well-known/agent-card.signature`, `GET /.well-known/agent-policy.json`, `GET /.well-known/capability-graph.json` |
| Reputation, SLA and episodes | `GET /agent/v1/reputation`, `GET /agent/v1/sla`, `GET /agent/v1/episodes` |
| x402 capability execution | `GET /.well-known/x402.json`, `POST /x402/capabilities/{capability}/execute` |
| Multi-registry discovery | `GET /agent/v1/registries`, `GET /.well-known/agntcy.json`, `GET /.well-known/oasf.json`, `GET /agent/v1/registry-records/agntcy-oasf` |
| Agent Pay | `POST /agent/v1/pay`, `GET /agent/v1/pay/{id}` |
| Gas Station | `GET /v1/gas/status`, `GET /v1/gas/quote`, `POST /v1/gas/relay`, `GET /v1/gas/relay/{id}` |

Agents and developer backends should use Bearer API keys issued by the Developer Console.

For true agent interoperability tests, use `tools/agent-qa/openai-agent-pay-test`. It starts from the public Agent Card only and validates discovery, trust, policy, A2A task lifecycle, x402 challenge, registry records and payment intent creation.
