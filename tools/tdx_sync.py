#!/usr/bin/env python3
"""
tdx_sync.py — macOS 离线数据同步与本地复盘工具
从 Windows tdx-agent 纯数据层拉取全市场横截面日线、上证指数与码表，
增量写入本地 SQLite（~/.tdx_local_data/market.db），关闭虚拟机后仍可离线复盘。

用法:
  python3 tools/tdx_sync.py sync --days 120     # 增量同步最近 120 个交易日
  python3 tools/tdx_sync.py breadth --days 20   # 离线统计最近 20 个交易日下跌家数与情绪广度
"""

import argparse
import csv
import gzip
import io
import json
import os
import re
import sqlite3
import subprocess
import sys
import urllib.request
from pathlib import Path

DB_PATH = Path.home() / ".tdx_local_data" / "market.db"


def detect_vm_ip() -> str:
    env_ip = os.environ.get("TDX_AGENT_HOST", "").strip()
    if env_ip:
        return env_ip
    out = subprocess.check_output(["/usr/local/bin/prlctl", "list", "-a", "-f"], text=True, timeout=5)
    for line in out.splitlines():
        if "running" in line.lower() and "windows" in line.lower():
            m = re.search(r"(\d+\.\d+\.\d+\.\d+)", line)
            if m:
                return m.group(1)
    raise RuntimeError("未检测到运行中的 Parallels Windows 虚拟机（也可通过环境变量 TDX_AGENT_HOST 指定）")


def http_get(url: str, timeout: int = 30) -> bytes:
    req = urllib.request.Request(url, headers={"Accept-Encoding": "gzip"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        data = resp.read()
        if resp.headers.get("Content-Encoding") == "gzip":
            data = gzip.decompress(data)
        return data


def init_db(conn: sqlite3.Connection) -> None:
    conn.executescript(
        """
        PRAGMA journal_mode=WAL;
        CREATE TABLE IF NOT EXISTS daily_bars (
            date INTEGER NOT NULL,
            key TEXT NOT NULL,
            board TEXT NOT NULL,
            open REAL,
            high REAL,
            low REAL,
            close REAL,
            prev_close REAL,
            amount REAL,
            volume INTEGER,
            PRIMARY KEY (date, key)
        );
        CREATE INDEX IF NOT EXISTS idx_daily_date ON daily_bars(date);
        CREATE INDEX IF NOT EXISTS idx_daily_key ON daily_bars(key);

        CREATE TABLE IF NOT EXISTS index_bars (
            date INTEGER NOT NULL,
            key TEXT NOT NULL,
            open REAL,
            high REAL,
            low REAL,
            close REAL,
            amount REAL,
            volume INTEGER,
            PRIMARY KEY (date, key)
        );

        CREATE TABLE IF NOT EXISTS symbols (
            key TEXT PRIMARY KEY,
            name TEXT NOT NULL
        );
        """
    )


def parse_tnf_names(raw: bytes, mkt_prefix: str) -> list[tuple[str, str]]:
    out = []
    stride = 360
    for i in range(50, len(raw) - stride + 1, stride):
        code = raw[i : i + 6].decode("latin1", errors="ignore").strip()
        if len(code) != 6 or not code.isdigit():
            continue
        name_raw = raw[i + 31 : i + 47].split(b"\x00", 1)[0]
        name = name_raw.decode("gb18030", errors="ignore").strip()
        if name:
            out.append((mkt_prefix + code, name))
    return out


def cmd_sync(args: argparse.Namespace) -> None:
    ip = detect_vm_ip()
    base = f"http://{ip}:19876"
    DB_PATH.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(DB_PATH)
    init_db(conn)

    # 1. 拉取全市场横截面日线 (gzip CSV)
    url = f"{base}/data/cross-section?days={args.days}&format=csv"
    print(f"拉取全市场横截面数据: {url} ...")
    raw_csv = http_get(url, timeout=60).decode("utf-8")
    reader = csv.reader(io.StringIO(raw_csv))
    next(reader, None)  # header
    rows = []
    for r in reader:
        if len(r) < 10:
            continue
        rows.append((
            int(r[0]), r[1], r[2],
            float(r[3]), float(r[4]), float(r[5]), float(r[6]), float(r[7]),
            float(r[8]), int(r[9]),
        ))
    with conn:
        conn.executemany(
            "INSERT OR REPLACE INTO daily_bars VALUES (?,?,?,?,?,?,?,?,?,?)",
            rows,
        )
    print(f"已写入/更新 daily_bars: {len(rows):,} 条记录")

    # 2. 拉取上证指数 (1999999)
    idx_json = json.loads(http_get(f"{base}/data/bars?key=1999999&period=day&limit={args.days + 10}").decode("utf-8"))
    idx_rows = [
        (int(b["date"]), "1999999", b["open"], b["high"], b["low"], b["close"], b["amount"], b["volume"])
        for b in idx_json.get("bars", [])
    ]
    with conn:
        conn.executemany("INSERT OR REPLACE INTO index_bars VALUES (?,?,?,?,?,?,?,?)", idx_rows)
    print(f"已写入/更新上证指数: {len(idx_rows)} 天")

    # 3. 拉取码表名称
    sym_rows = []
    for fname, pfx in [("shs.tnf", "1"), ("szs.tnf", "0"), ("bjs.tnf", "2")]:
        try:
            sym_rows.extend(parse_tnf_names(http_get(f"{base}/file?name={fname}"), pfx))
        except Exception:
            pass
    if sym_rows:
        with conn:
            conn.executemany("INSERT OR REPLACE INTO symbols VALUES (?,?)", sym_rows)
        print(f"已更新码表 symbols: {len(sym_rows):,} 条")

    latest = conn.execute("SELECT MAX(date), COUNT(DISTINCT date) FROM daily_bars").fetchone()
    conn.close()
    print(f"离线库就绪: {DB_PATH} (最新日期 {latest[0]}，共 {latest[1]} 个交易日)")


def cmd_breadth(args: argparse.Namespace) -> None:
    if not DB_PATH.exists():
        print(f"离线库不存在: {DB_PATH}，请先运行: python3 tools/tdx_sync.py sync", file=sys.stderr)
        sys.exit(1)
    conn = sqlite3.connect(DB_PATH)

    # 上证指数对照
    sh_rows = conn.execute("SELECT date, close FROM index_bars WHERE key='1999999' ORDER BY date").fetchall()
    sh_map = {}
    for i, (d, c) in enumerate(sh_rows):
        pct = (c - sh_rows[i - 1][1]) * 100.0 / sh_rows[i - 1][1] if i > 0 and sh_rows[i - 1][1] > 0 else 0.0
        sh_map[d] = (c, pct)

    dates = [
        r[0] for r in conn.execute(
            "SELECT date FROM daily_bars GROUP BY date HAVING COUNT(*) >= 1000 ORDER BY date DESC LIMIT ?",
            (args.days,),
        ).fetchall()
    ]
    dates.reverse()

    print(f"{'日期':<10} {'涨家':>5} {'跌家':>5} {'跌比%':>6} {'中位%':>7} {'跌>3%':>5} {'跌>5%':>5} {'跌停':>4} {'涨停':>4} {'成交(亿)':>8} {'上证(涨跌%)':>16}")
    print("-" * 92)
    for d in dates:
        bars = conn.execute(
            "SELECT board, close, prev_close, amount FROM daily_bars WHERE date=? AND prev_close > 0",
            (d,),
        ).fetchall()
        up = down = d3 = d5 = ld = lu = 0
        amt = 0.0
        pcts = []
        for board, c, pc, a in bars:
            pct = (c - pc) * 100.0 / pc
            pcts.append(pct)
            amt += a
            if c > pc:
                up += 1
            elif c < pc:
                down += 1
            if pct <= -3.0:
                d3 += 1
            if pct <= -5.0:
                d5 += 1
            lim = 19.6 if board == "20cm" else (29.5 if board == "30cm" else 9.7)
            if pct <= -lim:
                ld += 1
            if pct >= lim:
                lu += 1
        pcts.sort()
        total = len(bars)
        med = pcts[total // 2] if total else 0.0
        dp = down * 100.0 / total if total else 0.0
        sh_c, sh_p = sh_map.get(d, (0.0, 0.0))
        print(f"{d:<10} {up:>5} {down:>5} {dp:>5.1f}% {med:>+6.2f}% {d3:>5} {d5:>5} {ld:>4} {lu:>4} {amt/1e8:>8.0f} {sh_c:>8.2f} ({sh_p:>+5.2f}%)")
    conn.close()


def main() -> None:
    p = argparse.ArgumentParser(description="TDX 本地离线数据同步与复盘工具")
    sub = p.add_subparsers(dest="cmd", required=True)

    p_sync = sub.add_parser("sync", help="从 Windows tdx-agent 增量同步至本地 SQLite")
    p_sync.add_argument("--days", type=int, default=120, help="同步最近 N 个交易日 (默认 120)")
    p_sync.set_defaults(func=cmd_sync)

    p_br = sub.add_parser("breadth", help="离线查看每日下跌家数与情绪广度")
    p_br.add_argument("--days", type=int, default=20, help="展示最近 N 个交易日 (默认 20)")
    p_br.set_defaults(func=cmd_breadth)

    args = p.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
