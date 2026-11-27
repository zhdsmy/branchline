#!/usr/bin/env python3
"""Controlled TCP latency proxy for Branchline SSH performance measurements.

Model:
- Listens on a local loopback port (127.0.0.1) and forwards TCP streams to a
  verified non-loopback Linux container IP:port (e.g., 192.168.64.x:22).
- Never inspects, alters, or fabricates SSH protocol payloads; strict host-key
  and user-key verification remain end-to-end between OpenSSH client and server.
- Applies a symmetric one-way propagation delay (rtt_ms / 2.0 in each direction:
  client->server and server->client) using arrival-timestamped FIFO queues.
- Each read chunk is stamped upon arrival at monotonic time t_read with
  t_release = max(t_read + one_way_delay, prev_t_release). The reader loop
  never sleeps; a dedicated writer loop per direction awaits t_release before
  forwarding. Continuous byte streams are therefore shifted in availability by
  one_way_delay without accumulating serial per-chunk sleep delays.
"""

from __future__ import annotations

import argparse
import asyncio
import ipaddress
import json
import os
import signal
import socket
import sys
import threading
import time


def validate_target_ip(raw_host: str) -> str:
    lowered = raw_host.strip().lower()
    if lowered in ("", "localhost", "e4"):
        raise ValueError(f"forbidden or loopback target host: {raw_host!r}")
    try:
        addr = ipaddress.ip_address(raw_host.strip())
    except ValueError as exc:
        raise ValueError(
            f"target-host must be a literal non-loopback IPv4 address, got {raw_host!r}"
        ) from exc
    if addr.version != 4 or addr.is_loopback or addr.is_unspecified:
        raise ValueError(
            f"target-host must be a non-loopback IPv4 address, got {raw_host!r}"
        )
    return str(addr)


class LatencyProxy:
    def __init__(
        self,
        listen_host: str,
        listen_port: int,
        target_host: str,
        target_port: int,
        rtt_ms: float,
        chunk_size: int,
        events_file: str | None,
    ) -> None:
        self.listen_host = listen_host
        self.listen_port = listen_port
        self.target_host = target_host
        self.target_port = target_port
        self.rtt_ms = float(rtt_ms)
        self.one_way_sec = (self.rtt_ms / 2.0) / 1000.0
        self.chunk_size = chunk_size
        self.events_file = events_file
        self._events_fp = open(events_file, "a", encoding="utf-8") if events_file else None
        self._next_conn_id = 0
        self._active_tasks: set[asyncio.Task] = set()
        self._server: asyncio.AbstractServer | None = None
        self._stop_event: asyncio.Event | None = None

    def _log_event(self, payload: dict) -> None:
        if self._events_fp is None:
            return
        record = {
            "ts_unix_ns": time.time_ns(),
            "ts_mono_ns": time.monotonic_ns(),
            **payload,
        }
        self._events_fp.write(json.dumps(record, sort_keys=True) + "\n")
        self._events_fp.flush()

    @staticmethod
    def _set_nodelay(writer: asyncio.StreamWriter) -> None:
        sock = writer.get_extra_info("socket")
        if sock is not None:
            try:
                sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
            except OSError:
                pass

    async def _pipe_direction(
        self,
        conn_id: int,
        direction: str,
        reader: asyncio.StreamReader,
        writer: asyncio.StreamWriter,
        stats: dict,
    ) -> None:
        queue: asyncio.Queue[tuple[float, bytes | None]] = asyncio.Queue()
        delay = self.one_way_sec

        async def read_loop() -> None:
            last_release = 0.0
            try:
                while True:
                    data = await reader.read(self.chunk_size)
                    now = time.monotonic()
                    release_at = now + delay
                    if release_at < last_release:
                        release_at = last_release
                    last_release = release_at
                    if not data:
                        await queue.put((release_at, None))
                        break
                    stats[f"chunks_{direction}"] += 1
                    stats[f"bytes_{direction}"] += len(data)
                    await queue.put((release_at, data))
            except ( asyncio.CancelledError, ConnectionError, OSError ):
                await queue.put((time.monotonic(), None))

        async def write_loop() -> None:
            try:
                while True:
                    release_at, data = await queue.get()
                    now = time.monotonic()
                    wait_sec = release_at - now
                    if wait_sec > 0:
                        await asyncio.sleep(wait_sec)
                    if data is None:
                        if writer.can_write_eof():
                            try:
                                writer.write_eof()
                            except OSError:
                                pass
                        break
                    writer.write(data)
                    # Coalesce any additional chunks whose release timestamp has already arrived.
                    while not queue.empty():
                        next_release, next_data = queue._queue[0]  # type: ignore[attr-defined]
                        if next_release > time.monotonic():
                            break
                        queue.get_nowait()
                        if next_data is None:
                            await writer.drain()
                            if writer.can_write_eof():
                                try:
                                    writer.write_eof()
                                except OSError:
                                    pass
                            return
                        writer.write(next_data)
                    await writer.drain()
            except (asyncio.CancelledError, ConnectionError, OSError):
                pass

        read_task = asyncio.create_task(read_loop())
        write_task = asyncio.create_task(write_loop())
        try:
            await asyncio.gather(read_task, write_task)
        finally:
            for t in (read_task, write_task):
                if not t.done():
                    t.cancel()

    async def _handle_client(
        self, client_reader: asyncio.StreamReader, client_writer: asyncio.StreamWriter
    ) -> None:
        self._next_conn_id += 1
        conn_id = self._next_conn_id
        self._set_nodelay(client_writer)
        peer = client_writer.get_extra_info("peername")
        peer_str = f"{peer[0]}:{peer[1]}" if isinstance(peer, tuple) and len(peer) >= 2 else str(peer)
        opened_mono_ns = time.monotonic_ns()
        self._log_event(
            {
                "event": "accept",
                "conn_id": conn_id,
                "peer": peer_str,
                "target": f"{self.target_host}:{self.target_port}",
                "rtt_ms": self.rtt_ms,
            }
        )
        upstream_writer: asyncio.StreamWriter | None = None
        stats = {
            "bytes_c2s": 0,
            "bytes_s2c": 0,
            "chunks_c2s": 0,
            "chunks_s2c": 0,
        }
        try:
            # Model TCP SYN/SYN-ACK handshake delay across the simulated link (one RTT
            # before client sees connected stream readiness, or one_way on connect).
            # Note: Local loopback TCP handshake completes in kernel on accept(), so the
            # first application packet round-trip experiences c2s (one_way) + s2c (one_way) = rtt_ms.
            upstream_reader, upstream_writer = await asyncio.wait_for(
                asyncio.open_connection(self.target_host, self.target_port),
                timeout=15.0,
            )
            self._set_nodelay(upstream_writer)
            self._log_event(
                {
                    "event": "connected_upstream",
                    "conn_id": conn_id,
                    "target": f"{self.target_host}:{self.target_port}",
                }
            )
            await asyncio.gather(
                self._pipe_direction(conn_id, "c2s", client_reader, upstream_writer, stats),
                self._pipe_direction(conn_id, "s2c", upstream_reader, client_writer, stats),
            )
        except Exception as exc:
            self._log_event(
                {
                    "event": "error",
                    "conn_id": conn_id,
                    "error": str(exc),
                }
            )
        finally:
            for w in (upstream_writer, client_writer):
                if w is not None:
                    try:
                        w.close()
                        await w.wait_closed()
                    except Exception:
                        pass
            duration_ms = (time.monotonic_ns() - opened_mono_ns) / 1e6
            self._log_event(
                {
                    "event": "closed",
                    "conn_id": conn_id,
                    "duration_ms": round(duration_ms, 3),
                    **stats,
                }
            )

    def _spawn_client_task(
        self, client_reader: asyncio.StreamReader, client_writer: asyncio.StreamWriter
    ) -> None:
        task = asyncio.create_task(self._handle_client(client_reader, client_writer))
        self._active_tasks.add(task)
        task.add_done_callback(self._active_tasks.discard)

    async def start(self, ready_file: str | None, watch_stdin: bool) -> None:
        stop_event = asyncio.Event()
        self._stop_event = stop_event
        self._server = await asyncio.start_server(
            self._spawn_client_task,
            host=self.listen_host,
            port=self.listen_port,
        )
        socks = self._server.sockets or []
        if not socks:
            raise RuntimeError("proxy failed to bind listening socket")
        bound_addr = socks[0].getsockname()
        bound_host, bound_port = bound_addr[0], int(bound_addr[1])

        info = {
            "status": "ready",
            "pid": os.getpid(),
            "listen_host": bound_host,
            "listen_port": bound_port,
            "target_host": self.target_host,
            "target_port": self.target_port,
            "rtt_ms": self.rtt_ms,
            "one_way_ms": self.rtt_ms / 2.0,
            "events_file": self.events_file or "",
        }
        if ready_file:
            tmp_path = f"{ready_file}.tmp.{os.getpid()}"
            with open(tmp_path, "w", encoding="utf-8") as fp:
                fp.write(json.dumps(info, sort_keys=True) + "\n")
            os.replace(tmp_path, ready_file)
        print(json.dumps(info, sort_keys=True), flush=True)
        self._log_event({"event": "proxy_started", **info})

        loop = asyncio.get_running_loop()
        for sig in (signal.SIGINT, signal.SIGTERM):
            try:
                loop.add_signal_handler(sig, stop_event.set)
            except NotImplementedError:
                pass

        if watch_stdin:
            self._start_stdin_eof_watcher(loop, stop_event)

        await stop_event.wait()
        await self.shutdown()

    def _start_stdin_eof_watcher(
        self, loop: asyncio.AbstractEventLoop, stop_event: asyncio.Event
    ) -> None:
        def _reader() -> None:
            try:
                while sys.stdin.buffer.read(4096):
                    pass
            except Exception:
                pass
            try:
                loop.call_soon_threadsafe(stop_event.set)
            except RuntimeError:
                pass

        t = threading.Thread(target=_reader, name="perf-proxy-stdin-eof", daemon=True)
        t.start()

    async def shutdown(self) -> None:
        if self._server is not None:
            self._server.close()
            await self._server.wait_closed()
        if self._active_tasks:
            for t in list(self._active_tasks):
                t.cancel()
            await asyncio.gather(*self._active_tasks, return_exceptions=True)
        self._log_event(
            {
                "event": "proxy_stopped",
                "total_connections": self._next_conn_id,
            }
        )
        if self._events_fp is not None:
            self._events_fp.close()
            self._events_fp = None


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--target-host",
        required=True,
        help="Non-loopback IPv4 address of the Linux container (e.g. 192.168.64.x)",
    )
    parser.add_argument(
        "--target-port",
        type=int,
        default=22,
        help="Target SSH TCP port (default: 22)",
    )
    parser.add_argument(
        "--listen-host",
        default="127.0.0.1",
        help="Local loopback bind address (default: 127.0.0.1)",
    )
    parser.add_argument(
        "--listen-port",
        type=int,
        default=0,
        help="Local loopback TCP port (default: 0 for ephemeral)",
    )
    parser.add_argument(
        "--rtt-ms",
        type=float,
        required=True,
        help="Added round-trip latency in milliseconds (split equally per direction)",
    )
    parser.add_argument(
        "--chunk-size",
        type=int,
        default=65536,
        help="Read buffer chunk size in bytes (default: 65536)",
    )
    parser.add_argument(
        "--ready-file",
        default="",
        help="Optional path to write startup JSON atomically when ready",
    )
    parser.add_argument(
        "--events-file",
        default="",
        help="Optional JSONL path recording TCP connection & byte events",
    )
    parser.add_argument(
        "--exit-on-stdin-eof",
        action="store_true",
        help="Exit automatically when stdin closes (prevents orphan proxy processes)",
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    try:
        target_host = validate_target_ip(args.target_host)
        listen_addr = ipaddress.ip_address(args.listen_host)
        if not listen_addr.is_loopback:
            raise ValueError(f"listen-host must be loopback, got {args.listen_host!r}")
        if not (1 <= args.target_port <= 65535):
            raise ValueError(f"target-port out of range: {args.target_port}")
        if not (0 <= args.listen_port <= 65535):
            raise ValueError(f"listen-port out of range: {args.listen_port}")
        if args.rtt_ms < 0 or args.rtt_ms > 5000:
            raise ValueError(f"rtt-ms out of range [0, 5000]: {args.rtt_ms}")
        if args.chunk_size < 1024:
            raise ValueError(f"chunk-size must be >= 1024, got {args.chunk_size}")
    except ValueError as err:
        print(f"perf-proxy: {err}", file=sys.stderr)
        return 2

    proxy = LatencyProxy(
        listen_host=str(listen_addr),
        listen_port=args.listen_port,
        target_host=target_host,
        target_port=args.target_port,
        rtt_ms=args.rtt_ms,
        chunk_size=args.chunk_size,
        events_file=args.events_file or None,
    )
    try:
        asyncio.run(proxy.start(args.ready_file or None, args.exit_on_stdin_eof))
    except KeyboardInterrupt:
        pass
    except Exception as err:
        print(f"perf-proxy: runtime error: {err}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
