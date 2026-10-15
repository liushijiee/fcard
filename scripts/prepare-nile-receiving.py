"""Review the configured test asset and emit the offline gateway allowlist."""
import hashlib
import json
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urlsplit
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]
CONFIG = ROOT / ".env.deposits"
GENESIS = "0000000000000000d698d4192c56cb6be724a558448e2684802de4d6cd8690dc"
CONTRACT = "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf"
BYTECODE_HASH = "2866816060782939f36b0f42dd695c197b72d4ea133f433a08575c9a528a93b5"
ALPHABET = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"


def address_hex(address):
    if len(address) != 34 or not address.startswith("T"):
        raise ValueError("invalid Nile public address")
    number = 0
    for character in address:
        number = number * 58 + ALPHABET.index(character)
    raw = number.to_bytes(25, "big")
    checksum = hashlib.sha256(hashlib.sha256(raw[:21]).digest()).digest()[:4]
    if raw[0] != 0x41 or raw[21:] != checksum or raw[1:21] == bytes(20):
        raise ValueError("invalid Nile address checksum")
    return raw[:21].hex()


def rpc(path, data):
    request = Request("https://nile.trongrid.io/" + path, data=json.dumps(data).encode(), headers={"Content-Type": "application/json"})
    with urlopen(request, timeout=20) as response:
        if "application/json" not in response.headers.get("Content-Type", ""):
            raise ValueError("unexpected RPC content type")
        return json.load(response)


lines = CONFIG.read_text(encoding="utf-8-sig").splitlines()
settings = dict(line.split("=", 1) for line in lines if line and not line.startswith("#") and "=" in line)
if settings.get("DEPOSIT_ENVIRONMENT") != "nile" or settings.get("DEPOSIT_CREATE_ENABLED") != "false":
    raise ValueError("review requires Nile with creation disabled")
if settings.get("DEPOSIT_CONTRACT") != CONTRACT:
    raise ValueError("contract differs from the user's previously reviewed test asset")
wallets = settings["DEPOSIT_RECEIVING_WALLETS"].split(",")
if not wallets or len(wallets) != len(set(wallets)):
    raise ValueError("missing or duplicate wallets")
for wallet in wallets:
    address_hex(wallet)
origin = settings["EPUSDT_CHECKOUT_ORIGIN"]
url = urlsplit(origin)
if url.scheme != "https" or not url.netloc or url.path or url.query or url.fragment or url.username:
    raise ValueError("HTTPS root checkout origin required")
genesis = rpc("walletsolidity/getblockbynum", {"num": 0, "visible": False})
if genesis.get("blockID") != GENESIS:
    raise ValueError("wrong chain identity")
contract = rpc("wallet/getcontract", {"value": CONTRACT, "visible": True})
returned = contract.get("contract_address", "")
if returned != CONTRACT and returned.lower() != address_hex(CONTRACT):
    raise ValueError("contract address mismatch")
bytecode = bytes.fromhex(contract.get("bytecode", ""))
if not bytecode or hashlib.sha256(bytecode).hexdigest() != BYTECODE_HASH:
    raise ValueError("contract bytecode differs from the recorded review")
abi = contract.get("abi", {}).get("entrys", [])
if not {"transfer", "decimals"}.issubset({entry.get("name") for entry in abi}):
    raise ValueError("token ABI mismatch")
decimals = rpc("wallet/triggerconstantcontract", {"owner_address": wallets[0], "contract_address": CONTRACT, "function_selector": "decimals()", "visible": True})
if not decimals.get("result", {}).get("result") or len(decimals.get("constant_result", [])) != 1 or int(decimals["constant_result"][0], 16) != 6:
    raise ValueError("token precision mismatch")
review = json.loads((ROOT / ".cache/nile-live-review/review-result.json").read_text())
checkpoint = review["checkpoint"]
anchor = rpc("walletsolidity/getblockbynum", {"num": checkpoint["Height"], "visible": False})
if anchor.get("blockID") != checkpoint["BlockID"] or review["blocks"] < 10:
    raise ValueError("checkpoint or contiguous review mismatch")
policy = {"environment": "nile", "genesis": GENESIS, "contract": CONTRACT, "decimals": 6, "bytecode_sha256": BYTECODE_HASH, "wallets": wallets, "checkout_origin": origin, "reviewed_at": datetime.now(timezone.utc).isoformat()}
destination = ROOT / ".cache/epusdt-testnet/conf/nile-receiving.json"
destination.write_text(json.dumps(policy, indent=2) + "\n", encoding="utf-8")
updates = {"DEPOSIT_ASSET_REVIEWED": "true", "DEPOSIT_SCAN_START_HEIGHT": str(checkpoint["Height"]), "DEPOSIT_SCAN_START_BLOCK_ID": checkpoint["BlockID"]}
new_lines = []
for line in lines:
    key = line.split("=", 1)[0]
    new_lines.append(key + "=" + updates.pop(key) if key in updates else line)
new_lines.extend(key + "=" + value for key, value in updates.items())
CONFIG.write_text("\n".join(new_lines) + "\n", encoding="utf-8")
print("Nile identity, pinned token bytecode/ABI/decimals, wallet checksums and original checkpoint reviewed.")
print("Offline gateway allowlist prepared; runtime and creation remain disabled.")
