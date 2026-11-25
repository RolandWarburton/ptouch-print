#!/usr/bin/env python3
"""Turns a board's settings.toml into the CSV that nvs_partition_gen.py reads.

usage: settings_to_nvs.py settings.toml out.csv

Checks what the firmware (components/board/settings.c) would otherwise only
complain about when the board boots.
"""
import csv
import sys
import tomllib

MAX_NETWORKS = 4
KEYS = {"server", "token", "media", "wifi"}


def fail(msg):
    sys.exit(f"settings: {msg}")


def text(table, key, limit, where="", required=True):
    value = table.get(key)
    name = where + key
    if value is None:
        if required:
            fail(f"{name} is missing")
        return None
    if not isinstance(value, str):
        fail(f"{name} must be a string")
    if len(value.encode()) > limit:
        fail(f"{name} is longer than {limit} bytes")
    return value


def load(path):
    with open(path, "rb") as f:
        try:
            cfg = tomllib.load(f)
        except tomllib.TOMLDecodeError as e:
            fail(f"{path}: {e}")

    if extra := set(cfg) - KEYS:
        fail(f"unknown setting {sorted(extra)[0]!r}")

    rows = [("board", "namespace", "", "")]

    server = text(cfg, "server", 127)
    if not server.startswith("https://"):
        fail("server must be an https:// URL")
    rows.append(("server", "data", "string", server))
    if not text(cfg, "token", 127):
        fail("token is empty")
    rows.append(("token", "data", "string", cfg["token"]))

    if (media := text(cfg, "media", 32, required=False)) is not None:
        rows.append(("media", "data", "string", media))

    wifi = cfg.get("wifi")
    if not isinstance(wifi, list) or not wifi:
        fail("add at least one [[wifi]] network")
    if len(wifi) > MAX_NETWORKS:
        fail(f"at most {MAX_NETWORKS} [[wifi]] networks")
    for i, net in enumerate(wifi, 1):
        where = f"wifi {i}: "
        if extra := set(net) - {"ssid", "pass"}:
            fail(f"{where}unknown setting {sorted(extra)[0]!r}")
        if not text(net, "ssid", 32, where):
            fail(f"{where}ssid is empty")
        rows.append((f"ssid{i}", "data", "string", net["ssid"]))
        rows.append((f"pass{i}", "data", "string", text(net, "pass", 64, where)))
    return rows


def main():
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    rows = load(sys.argv[1])
    with open(sys.argv[2], "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(("key", "type", "encoding", "value"))
        w.writerows(rows)


main()
