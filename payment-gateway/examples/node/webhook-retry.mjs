import { ChainFX } from "../../sdk/node/index.js";

const chainfx = new ChainFX({
  apiKey: process.env.CHAINFX_API_KEY || "sk_test_chainfx_local",
  baseUrl: process.env.CHAINFX_API_BASE_URL || "http://localhost:8080"
});

const result = await chainfx.retryWebhook({
  orderId: process.env.CHAINFX_ORDER_ID,
  side: process.env.CHAINFX_ORDER_SIDE || "buy",
  event: "payment.completed",
  targetUrl: process.env.CHAINFX_WEBHOOK_TARGET_URL
});

console.log(result);
