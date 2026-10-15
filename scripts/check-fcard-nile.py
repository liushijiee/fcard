"""Check admission and reject an invalid callback; never submit an order."""
import argparse
import json
from urllib.error import HTTPError
from urllib.request import Request, urlopen

parser = argparse.ArgumentParser()
parser.add_argument("origin")
parser.add_argument("--frontend", action="store_true")
args = parser.parse_args()
origin = args.origin.rstrip("/")
headers = {"User-Agent": "Go-http-client/1.1"}
with urlopen(Request(origin + "/fcard/health/deposits", headers=headers), timeout=12) as response:
    health = json.load(response)["data"]
if not all(health.get(key) is True for key in ("available", "ready", "enabled")) or health.get("environment") != "nile" or health.get("max_amount") != "1" or health.get("balance_crediting") is not False:
    raise ValueError("Nile test admission is not ready")
print(json.dumps({"health": health}), flush=True)
request = Request(origin + "/api/v1/deposits/notify", data=b"{}", headers={**headers, "Content-Type": "application/json"})
try:
    with urlopen(request, timeout=12) as response:
        raise ValueError("invalid callback unexpectedly accepted: " + str(response.status))
except HTTPError as error:
    if error.code not in (400, 401):
        raise ValueError("unexpected callback routing status: " + str(error.code))
    print(json.dumps({"invalid_callback_status": error.code, "orders_created": False}), flush=True)
if args.frontend:
    with urlopen("http://127.0.0.1:5173/portal/deposits", timeout=5) as response:
        if response.status != 200:
            raise ValueError("local frontend is unavailable")
        print("local user deposit page available", flush=True)
