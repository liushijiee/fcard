"""Log in with sandbox API credentials and fetch all cardholder IDs."""

import json
import os
import sys
from datetime import datetime, timezone
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import Request, urlopen


def load_dotenv(path='.env'):
    if not os.path.exists(path):
        return
    with open(path, encoding='utf-8-sig') as dotenv_file:
        for raw_line in dotenv_file:
            line = raw_line.strip()
            if not line or line.startswith('#') or '=' not in line:
                continue
            name, value = line.split('=', 1)
            name = name.strip()
            value = value.strip().strip('\"').strip("'")
            if name and name not in os.environ:
                os.environ[name] = value


def required_env(name):
    value = os.environ.get(name, '').strip()
    if not value:
        raise ValueError(f'Missing required environment variable: {name}')
    return value


def print_request(request):
    headers = {
        name: (
            'Bearer <REDACTED>' if name.lower() == 'authorization'
            else '<REDACTED>' if name.lower() in ('x-api-key', 'x-client-id')
            else value
        )
        for name, value in request.header_items()
    }
    print('\n=== REQUEST ===', file=sys.stderr)
    print('Time (UTC): ' + datetime.now(timezone.utc).isoformat(), file=sys.stderr)
    print('Method: ' + request.get_method(), file=sys.stderr)
    print('URL: ' + request.full_url, file=sys.stderr)
    print('Headers: ' + json.dumps(headers, ensure_ascii=False, indent=2), file=sys.stderr)
    body = request.data.decode('utf-8', errors='replace') if request.data else '<empty>'
    print('Body: ' + body, file=sys.stderr)


def print_response(response, body, redact_tokens=False):
    print('\n=== RESPONSE ===', file=sys.stderr)
    print('Time (UTC): ' + datetime.now(timezone.utc).isoformat(), file=sys.stderr)
    print(f'Status: {response.code} {response.reason}', file=sys.stderr)
    print('Headers:', file=sys.stderr)
    for name, value in response.headers.items():
        print(f'{name}: {value}', file=sys.stderr)
    print('Body:', file=sys.stderr)
    charset = response.headers.get_content_charset() or 'utf-8'
    try:
        text = body.decode(charset, errors='replace')
    except LookupError:
        text = body.decode('utf-8', errors='replace')
    if redact_tokens and text:
        try:
            data = json.loads(text)
            if isinstance(data, dict):
                for name in ('token', 'access_token', 'refresh_token'):
                    if name in data:
                        data[name] = '<REDACTED>'
                text = json.dumps(data, ensure_ascii=False, indent=2)
            else:
                text = '<login response body omitted for credential safety>'
        except ValueError:
            text = '<non-JSON login response body omitted for credential safety>'
    print(text or '<empty>', file=sys.stderr)


def request_json(request, redact_tokens=False):
    print_request(request)
    try:
        with urlopen(request, timeout=30) as response:
            body = response.read()
            print_response(response, body, redact_tokens=redact_tokens)
    except HTTPError as error:
        try:
            print_response(error, error.read(), redact_tokens=redact_tokens)
        finally:
            error.close()
        raise
    data = json.loads(body)
    if not isinstance(data, dict):
        raise ValueError('Response must be a JSON object.')
    return data


def get_access_token(client_id, api_key):
    request = Request(
        'https://api.sandbox.airwallex.com/api/v1/authentication/login',
        data=json.dumps({}).encode('utf-8'),
        headers={
            'x-client-id': client_id,
            'x-api-key': api_key,
            'Content-Type': 'application/json',
            'Accept': 'application/json',
        },
        method='POST',
    )
    response = request_json(request, redact_tokens=True)
    token = response.get('token')
    if not isinstance(token, str) or not token.strip():
        raise ValueError('Login response did not contain a valid token field.')
    return token


def get_cardholder_ids(access_token):
    cardholder_ids = []
    page_num = 0
    while True:
        query = urlencode({'page_num': page_num, 'page_size': 100})
        request = Request(
            'https://api.sandbox.airwallex.com/api/v1/issuing/cardholders?' + query,
            headers={'Authorization': 'Bearer ' + access_token, 'Accept': 'application/json'},
            method='GET',
        )
        data = request_json(request)
        items = data.get('items')
        has_more = data.get('has_more')
        if not isinstance(items, list) or not isinstance(has_more, bool):
            raise ValueError('Response must contain valid items and has_more fields.')
        for item in items:
            if not isinstance(item, dict):
                raise ValueError('Invalid cardholder record.')
            cardholder_id = item.get('cardholder_id')
            if not isinstance(cardholder_id, str) or not cardholder_id.strip():
                raise ValueError('Missing or invalid cardholder_id.')
            cardholder_ids.append(cardholder_id)
        if not has_more:
            return cardholder_ids
        page_num += 1


def main():
    try:
        load_dotenv()
        client_id = required_env('AIRWALLEX_CLIENT_ID')
        api_key = required_env('AIRWALLEX_API_KEY')
        access_token = get_access_token(client_id, api_key)
        cardholder_ids = get_cardholder_ids(access_token)
    except HTTPError as error:
        print(f'Request failed: HTTP {error.code}. Check sandbox token and permissions.', file=sys.stderr)
        return 1
    except (URLError, OSError, ValueError) as error:
        print(f'Failed to fetch cardholders: {error}', file=sys.stderr)
        return 1
    except (KeyboardInterrupt, EOFError):
        print('\nCancelled.', file=sys.stderr)
        return 1
    print(json.dumps(cardholder_ids, ensure_ascii=False, indent=2))
    if not cardholder_ids:
        print('No cardholders found in this sandbox account.', file=sys.stderr)
    return 0


if __name__ == '__main__':
    sys.exit(main())
