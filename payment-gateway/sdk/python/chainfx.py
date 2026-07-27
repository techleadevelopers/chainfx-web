from __future__ import annotations

import json
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import Any, Optional


class ChainFXError(Exception):
    def __init__(self, message: str, status: Optional[int] = None, payload: Any = None):
        super().__init__(message)
        self.status = status
        self.payload = payload


@dataclass
class ChainFX:
    api_key: str
    base_url: str = "https://api.chainfx.com"
    timeout: int = 20

    def __post_init__(self) -> None:
        if not self.api_key:
            raise ChainFXError("api_key is required")
        self.base_url = self.base_url.rstrip("/")

    def rates(self) -> Any:
        return self._request("GET", "/rates")

    def quote(self, side: str = "buy", fiat: str = "BRL", asset: str = "USDT", amount: float = 0, payment_method: Optional[str] = None) -> Any:
        return self._request(
            "POST",
            "/quote",
            {"side": side, "fiat": fiat, "asset": asset, "amount": amount, "paymentMethod": payment_method},
        )

    def buy(self, fiat: str = "BRL", asset: str = "USDT", amount: float = 0, wallet: Optional[str] = None, payment_method: str = "pix", quote_id: Optional[str] = None, customer: Optional[dict] = None) -> Any:
        return self._request(
            "POST",
            "/buy",
            {
                "quoteId": quote_id,
                "fiat": fiat,
                "asset": asset,
                "amount": amount,
                "wallet": wallet,
                "paymentMethod": payment_method,
                "customer": customer,
            },
        )

    def sell(self, asset: str = "USDT", network: str = "BSC", amount: float = 0, amount_brl: Optional[float] = None, deposit_address: Optional[str] = None, pix_cpf: Optional[str] = None, pix_phone: Optional[str] = None, quote_id: Optional[str] = None) -> Any:
        return self._request(
            "POST",
            "/sell",
            {
                "quoteId": quote_id,
                "asset": asset,
                "network": network,
                "amount": amount,
                "amountBRL": amount_brl,
                "depositAddress": deposit_address,
                "pixCpf": pix_cpf,
                "pixPhone": pix_phone,
            },
        )

    def order(self, order_id: str, access_token: Optional[str] = None) -> Any:
        query = ""
        if access_token:
            query = "?" + urllib.parse.urlencode({"accessToken": access_token})
        return self._request("GET", f"/order/{urllib.parse.quote(order_id)}{query}")

    def test_webhook(self, event: str = "payment.completed", order_id: Optional[str] = None, asset: str = "USDT", amount: str = "96.52", target_url: Optional[str] = None) -> Any:
        return self._request("POST", "/webhooks/test", {"event": event, "orderId": order_id, "asset": asset, "amount": amount, "targetUrl": target_url})

    def retry_webhook(self, event: str = "payment.completed", order_id: Optional[str] = None, side: Optional[str] = None, target_url: Optional[str] = None, asset: Optional[str] = None, amount: Optional[str] = None) -> Any:
        return self._request("POST", "/webhooks/retry", {"event": event, "orderId": order_id, "side": side, "targetUrl": target_url, "asset": asset, "amount": amount})

    def developer_logs(self, limit: int = 100) -> Any:
        return self._request("GET", f"/developers/logs?{urllib.parse.urlencode({'limit': limit})}")

    def api_keys(self) -> Any:
        return self._request("GET", "/developers/api-keys")

    def _request(self, method: str, path: str, payload: Optional[dict] = None) -> Any:
        data = None
        headers = {
            "Authorization": f"Bearer {self.api_key}",
            "Accept": "application/json",
        }
        if payload is not None:
            data = json.dumps(payload).encode("utf-8")
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(f"{self.base_url}{path}", data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                body = response.read().decode("utf-8")
                return json.loads(body) if body else None
        except urllib.error.HTTPError as exc:
            body = exc.read().decode("utf-8")
            parsed = json.loads(body) if body else None
            message = parsed.get("error") if isinstance(parsed, dict) else str(exc)
            raise ChainFXError(message, status=exc.code, payload=parsed) from exc
