#!/usr/bin/env python3
"""Simulate a single-phase Airwallex Issuing card payment in sandbox.

Required environment variables:
  AIRWALLEX_CLIENT_ID
  AIRWALLEX_API_KEY
  AIRWALLEX_CARD_NUMBER

Example:
  $env:AIRWALLEX_CLIENT_ID = "your-client-id"
  $env:AIRWALLEX_API_KEY = "your-api-key"
  $env:AIRWALLEX_CARD_NUMBER = "your-sandbox-card-number"
  python .\test_airwallex_card_payment.py --amount 10 --currency USD
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


BASE_URL = "https://api.sandbox.airwallex.com"
TOKEN_PATH = "/api/v1/authentication/login"
SIMULATION_PATH = "/api/v1/simulation/issuing/create"

def load_dotenv(path: str = ".env") -> None:
    """Load simple KEY=VALUE entries without requiring python-dotenv."""
    dotenv_path = os.path.abspath(path)
    if not os.path.exists(dotenv_path):
        return

    with open(dotenv_path, "r", encoding="utf-8-sig") as dotenv_file:
        for raw_line in dotenv_file:
            line = raw_line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            name, value = line.split("=", 1)
            name = name.strip()
            value = value.strip().strip('"').strip("'")
            if name and name not in os.environ:
                os.environ[name] = value


def post_json(
    path: str,
    *,
    headers: dict[str, str],
    payload: dict[str, Any],
) -> dict[str, Any]:
    request = Request(
        url=f"{BASE_URL}{path}",
        data=json.dumps(payload).encode("utf-8"),
        headers={**headers, "Content-Type": "application/json"},
        method="POST",
    )

    try:
        with urlopen(request, timeout=30) as response:
            raw_body = response.read().decode("utf-8")
    except HTTPError as error:
        body = error.read().decode("utf-8", errors="replace")
        raise RuntimeError(
            f"Airwallex API returned HTTP {error.code}: {body}"
        ) from error
    except URLError as error:
        raise RuntimeError(f"Unable to reach Airwallex API: {error.reason}") from error

    try:
        return json.loads(raw_body)
    except json.JSONDecodeError as error:
        raise RuntimeError(f"Airwallex returned invalid JSON: {raw_body}") from error


def get_access_token(client_id: str, api_key: str) -> str:
    response = post_json(
        TOKEN_PATH,
        headers={
            "x-client-id": client_id,
            "x-api-key": api_key,
        },
        payload={},
    )

    token = response.get("token")
    if not token:
        raise RuntimeError(
            "Token response did not contain a token field: "
            f"{json.dumps(response, ensure_ascii=False)}"
        )
    return token


def simulate_single_phase_payment(
    access_token: str,
    *,
    card_number: str,
    amount: float,
    currency: str,
    merchant_category_code: str,
    merchant_info: str,
) -> dict[str, Any]:
    payload = {
        "card_id": "dfeb8367-5ce4-4c8f-8bcc-795a30dcda8b",
        # 卡号
        "card_number": card_number,
        # 不需要审批
        "single_phase": True,
        # 交易金额
        "transaction_amount": amount,
        "transaction_currency": currency.upper(),
        "merchant_category_code": merchant_category_code,
        "merchant_info": merchant_info,
        "auth_code": "123312",
    }

    return post_json(
        SIMULATION_PATH,
        headers={"Authorization": f"Bearer {access_token}"},
        payload=payload,
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Simulate a single-phase Airwallex sandbox card payment."
    )
    parser.add_argument(
        "--amount",
        type=float,
        default=20.0,
        help="Transaction amount, default: 10.00",
    )
    parser.add_argument(
        "--currency",
        default="USD",
        help="Transaction currency, default: USD",
    )
    parser.add_argument(
        "--mcc",
        default="0742",
        help="Merchant category code, default: 0742",
    )
    parser.add_argument(
        "--merchant-info",
        default="CARD_TX_GENERATOR TEST",
        help="Merchant description used by the simulator.",
    )
    return parser.parse_args()


def required_env(name: str) -> str:
    value = os.getenv(name)
    if not value:
        raise RuntimeError(f"Missing required environment variable: {name}")
    return value


def main() -> int:
    load_dotenv()
    args = parse_args()

    if args.amount <= 0:
        print("Amount must be greater than zero.", file=sys.stderr)
        return 2

    try:
        client_id = required_env("AIRWALLEX_CLIENT_ID")
        api_key = required_env("AIRWALLEX_API_KEY")
        card_number = required_env("AIRWALLEX_CARD_NUMBER")

        print("Getting Airwallex sandbox access token...")
        access_token = get_access_token(client_id, api_key)

        print(
            f"Simulating single-phase payment: "
            f"{args.amount:.2f} {args.currency.upper()}"
        )
        response = simulate_single_phase_payment(
            access_token,
            card_number=card_number,
            amount=args.amount,
            currency=args.currency,
            merchant_category_code=args.mcc,
            merchant_info=args.merchant_info,
        )

        print("Payment simulation response:")
        print(json.dumps(response, indent=2, ensure_ascii=False))
        return 0
    except RuntimeError as error:
        print(f"Error: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())



