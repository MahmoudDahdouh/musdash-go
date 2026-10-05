"""Tiny RFC 6455 client (stdlib) for the terminal tests."""
import base64, os, socket, struct, json, time

class WS:
    def __init__(self, host, port, path, cookies, origin=None, timeout=15, extra=None):
        self.s = socket.create_connection((host, port), timeout=timeout)
        key = base64.b64encode(os.urandom(16)).decode()
        h = [f"GET {path} HTTP/1.1", f"Host: {host}:{port}", "Upgrade: websocket", "Connection: Upgrade", f"Sec-WebSocket-Key: {key}", "Sec-WebSocket-Version: 13"]
        if origin is not None: h.append(f"Origin: {origin}")
        if cookies: h.append("Cookie: " + "; ".join(f"{k}={v}" for k, v in cookies.items()))
        for k, v in (extra or {}).items(): h.append(f"{k}: {v}")
        self.s.sendall(("\r\n".join(h) + "\r\n\r\n").encode())
        buf = b""
        while b"\r\n\r\n" not in buf:
            d = self.s.recv(4096)
            if not d: break
            buf += d
        head, _, self.buf = buf.partition(b"\r\n\r\n")
        self.status = int(head.split(b" ")[1]) if head else 0
        self.head = head.decode("latin1")
        self.body = self.buf if self.status != 101 else b""

    def send(self, data, op=1):
        if isinstance(data, str): data = data.encode()
        n = len(data); m = os.urandom(4)
        hdr = bytes([0x80 | op])
        if n < 126: hdr += bytes([0x80 | n])
        elif n < 65536: hdr += bytes([0x80 | 126]) + struct.pack(">H", n)
        else: hdr += bytes([0x80 | 127]) + struct.pack(">Q", n)
        self.s.sendall(hdr + m + bytes(b ^ m[i % 4] for i, b in enumerate(data)))

    def _need(self, n):
        while len(self.buf) < n:
            d = self.s.recv(65536)
            if not d: raise EOFError("closed")
            self.buf += d
        out, self.buf = self.buf[:n], self.buf[n:]
        return out

    def recv(self, timeout=10):
        self.s.settimeout(timeout)
        h = self._need(2); op = h[0] & 15; n = h[1] & 127
        if n == 126: n = struct.unpack(">H", self._need(2))[0]
        elif n == 127: n = struct.unpack(">Q", self._need(8))[0]
        data = self._need(n)
        if op == 8:
            code = struct.unpack(">H", data[:2])[0] if len(data) >= 2 else 0
            return ("close", code, data[2:].decode("utf8", "replace"))
        return ("text" if op == 1 else "bin" if op == 2 else "ping" if op == 9 else "pong" if op == 10 else op, data)

    def read_until(self, needle, timeout=10):
        end = time.time() + timeout; out = b""
        while time.time() < end:
            try: r = self.recv(max(0.2, end - time.time()))
            except (socket.timeout, EOFError): break
            if r[0] == "close": return out, r
            if r[0] in ("bin", "text"):
                chunk = r[1] if isinstance(r[1], bytes) else r[1].encode()
                out += chunk
                if b"\x1b[6n" in chunk: self.send(b"\x1b[1;1R", 2)
            if needle.encode() in out: return out, None
        return out, None

    def close(self):
        try: self.s.close()
        except Exception: pass
