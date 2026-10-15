"""Read-only probes for the shared ngrok origin; never create an order."""

import argparse
import json
import urllib.error
import urllib.request
from urllib.parse import urlsplit


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


parser = argparse.ArgumentParser()
parser.add_argument("origin")
args = parser.parse_args()
origin = args.origin.rstrip("/")
parsed = urlsplit(origin)
if parsed.scheme not in ("http", "https") or not parsed.netloc or parsed.path:
    parser.error("origin must be a root URL")
opener = urllib.request.build_opener(NoRedirect())
probes = [
    ("/fcard/health/live", 200, "json"),
    ("/fcard/health/ready", 200, "json"),
    ("/epusdt/payments/gmpay/v1/config", 200, "json"),
    ("/payments/gmpay/v1/config", 200, "json"),
    ("/pay/checkout-counter/fcard_https_probe", 301, None),
    ("/cashier/fcard_https_probe", 200, "html"),
    ("/admin/api/v1/wallets", 404, None),
    ("/epusdt/admin/api/v1/wallets", 404, None),
    ("/pay/switch-network", 404, None),
    ("/epusdt/pay/switch-network", 404, None),
]
failed = False
for path, expected, content_type in probes:
    request = urllib.request.Request(origin + path, headers={"User-Agent": "Go-http-client/1.1"})
    try:
        try:
            response = opener.open(request, timeout=12)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            body = response.read(128 * 1024)
            actual_type = response.headers.get("Content-Type", "")
            ok = response.code == expected and (content_type is None or content_type in actual_type)
            if content_type == "json" and "json" in actual_type:
                json.loads(body)
            location = response.headers.get("Location")
            if expected == 301:
                ok = ok and location == "/cashier/fcard_https_probe"
            print(json.dumps({"path": path, "status": response.code, "type": actual_type, "location": location, "ok": ok}))
            if not ok and "json" in actual_type:
                print(body.decode("utf-8", errors="replace")[:500])
            failed |= not ok
    except (OSError, ValueError) as error:
        print(json.dumps({"path": path, "error": str(error)}))
        failed = True
raise SystemExit(1 if failed else 0)
