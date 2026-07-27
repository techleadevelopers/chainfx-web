export class ChainFXError extends Error {
  constructor(message, { status, payload } = {}) {
    super(message);
    this.name = "ChainFXError";
    this.status = status;
    this.payload = payload;
  }
}

export class ChainFX {
  constructor({ apiKey, baseUrl = "https://api.chainfx.com", fetchImpl = globalThis.fetch } = {}) {
    if (!apiKey) {
      throw new ChainFXError("apiKey is required");
    }
    if (!fetchImpl) {
      throw new ChainFXError("fetch is required. Use Node 18+ or pass fetchImpl.");
    }
    this.apiKey = apiKey;
    this.baseUrl = baseUrl.replace(/\/+$/, "");
    this.fetch = fetchImpl;
  }

  rates() {
    return this.#request("GET", "/rates");
  }

  quote({ side = "buy", fiat = "BRL", asset = "USDT", amount, paymentMethod } = {}) {
    return this.#request("POST", "/quote", { side, fiat, asset, amount, paymentMethod });
  }

  buy({ quoteId, fiat = "BRL", asset = "USDT", amount, wallet, paymentMethod = "pix", customer } = {}) {
    return this.#request("POST", "/buy", { quoteId, fiat, asset, amount, wallet, paymentMethod, customer });
  }

  sell({ quoteId, asset = "USDT", network = "BSC", amount, amountBRL, depositAddress, wallet, pixCpf, pixPhone, pix } = {}) {
    return this.#request("POST", "/sell", {
      quoteId,
      asset,
      network,
      amount,
      amountBRL,
      depositAddress,
      wallet,
      pixCpf,
      pixPhone,
      pix
    });
  }

  order(id, { accessToken } = {}) {
    const query = accessToken ? `?accessToken=${encodeURIComponent(accessToken)}` : "";
    return this.#request("GET", `/order/${encodeURIComponent(id)}${query}`);
  }

  testWebhook({ event = "payment.completed", orderId, asset = "USDT", amount = "96.52", targetUrl } = {}) {
    return this.#request("POST", "/webhooks/test", { event, orderId, asset, amount, targetUrl });
  }

  retryWebhook({ event = "payment.completed", orderId, side, targetUrl, asset, amount } = {}) {
    return this.#request("POST", "/webhooks/retry", { event, orderId, side, targetUrl, asset, amount });
  }

  developerLogs({ limit = 100 } = {}) {
    return this.#request("GET", `/developers/logs?limit=${encodeURIComponent(limit)}`);
  }

  apiKeys() {
    return this.#request("GET", "/developers/api-keys");
  }

  async #request(method, path, body) {
    const headers = {
      Authorization: `Bearer ${this.apiKey}`,
      Accept: "application/json"
    };
    const init = { method, headers };
    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body);
    }
    const response = await this.fetch(`${this.baseUrl}${path}`, init);
    const text = await response.text();
    const payload = text ? JSON.parse(text) : null;
    if (!response.ok) {
      throw new ChainFXError(payload?.error || `ChainFX request failed with ${response.status}`, {
        status: response.status,
        payload
      });
    }
    return payload;
  }
}

export default ChainFX;
