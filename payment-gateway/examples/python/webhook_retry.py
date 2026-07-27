import os
import sys

sys.path.append(os.path.join(os.path.dirname(__file__), "..", "..", "sdk", "python"))

from chainfx import ChainFX


chainfx = ChainFX(
    api_key=os.getenv("CHAINFX_API_KEY", "sk_test_chainfx_local"),
    base_url=os.getenv("CHAINFX_API_BASE_URL", "http://localhost:8080"),
)

result = chainfx.retry_webhook(
    order_id=os.environ["CHAINFX_ORDER_ID"],
    side=os.getenv("CHAINFX_ORDER_SIDE", "buy"),
    event="payment.completed",
    target_url=os.getenv("CHAINFX_WEBHOOK_TARGET_URL"),
)
print(result)
