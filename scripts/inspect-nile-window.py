"""Collect public transaction-type/receipt samples without moving a scanner."""
import argparse
import json
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from urllib.request import Request, urlopen

parser = argparse.ArgumentParser()
parser.add_argument("first", type=int)
parser.add_argument("last", type=int)
args = parser.parse_args()
if args.first <= 0 or not 0 <= args.last - args.first < 4096:
    parser.error("invalid bounded block range")
directory = Path(__file__).resolve().parents[1] / ".cache/nile-live-review"


def rpc(path, data):
    for attempt in range(3):
        try:
            request = Request("https://nile.trongrid.io/walletsolidity/" + path, data=json.dumps(data).encode(), headers={"Content-Type": "application/json"})
            with urlopen(request, timeout=15) as response:
                return json.load(response)
        except OSError:
            if attempt == 2:
                raise
            time.sleep(0.5 * (attempt + 1))


if rpc("getblockbynum", {"num": 0, "visible": False}).get("blockID") != "0000000000000000d698d4192c56cb6be724a558448e2684802de4d6cd8690dc":
    raise ValueError("wrong Nile identity")
samples = {}
with ThreadPoolExecutor(max_workers=4) as executor:
    for block in executor.map(lambda height: rpc("getblockbynum", {"num": height, "visible": False}), range(args.first, args.last + 1)):
        for transaction in block.get("transactions", []):
            for contract in transaction.get("raw_data", {}).get("contract", []):
                kind = contract.get("type")
                if kind in samples:
                    continue
                receipt = rpc("gettransactioninfobyid", {"value": transaction["txID"], "visible": False})
                samples[kind] = {"type": kind, "height": block["block_header"]["raw_data"]["number"], "tx": transaction["txID"], "ret": transaction.get("ret"), "result": receipt.get("result"), "tvm_result": receipt.get("receipt", {}).get("result"), "logs": len(receipt.get("log", []))}
                (directory / ("native-" + transaction["txID"] + ".json")).write_text(json.dumps({"block": block, "receipt": receipt}), encoding="utf-8")
                print(json.dumps(samples[kind]), flush=True)
print(json.dumps({"first": args.first, "last": args.last, "types": sorted(samples)}), flush=True)
