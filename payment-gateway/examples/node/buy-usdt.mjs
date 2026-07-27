import { ChainFX } from "../../sdk/node/index.js";

const chainfx = new ChainFX({
  apiKey: process.env.CHAINFX_API_KEY || "sk_test_chainfx_local",
  baseUrl: process.env.CHAINFX_API_BASE_URL || "http://localhost:8080"
});

const quote = await chainfx.quote({
  side: "buy",
  fiat: "BRL",
  asset: "USDT",
  amount: 500
});

console.log("quote", quote);

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
      line2: "Apto 101",
      district: "Bela Vista",
      city: "Sao Paulo",
      state: "SP",
      postalCode: "01310100",
      country: "BR"
    }
  }
});

console.log("order", order);
