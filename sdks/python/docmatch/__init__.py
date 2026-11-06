"""Official docmatch client. Apache-2.0. No telemetry; keys belong on servers."""
import asyncio
import hashlib
import hmac
import random
import time
import uuid
from email.utils import parsedate_to_datetime
from urllib.parse import urlsplit, quote
import httpx
from .operations import OPERATIONS

class DocmatchError(Exception):
    def __init__(self, status: int, code: str, message: str, request_id=None):
        self.status, self.code, self.message, self.request_id = status, code, message, request_id
        super().__init__(f"{message} ({status} {code})")

def safe_url(value: str):
    url = urlsplit(value)
    if url.username or url.password or not url.hostname or not (url.scheme == "https" or (url.scheme == "http" and url.hostname in ("localhost", "127.0.0.1", "::1"))):
        raise ValueError("HTTPS required outside loopback")
    return url

def route(operation: str, id: str) -> str:
    return OPERATIONS[operation]["path"].replace("{id}", quote(id, safe=""))

def retry_delay(header, attempt):
    if header:
        try:
            return max(0, int(header))
        except ValueError:
            try:
                return max(0, parsedate_to_datetime(header).timestamp() - time.time())
            except (ValueError, TypeError, OverflowError):
                pass
    return .25 * 2 ** attempt + random.random() * .25

def verify_webhook(body: bytes, header: str, secret: str, now=None) -> bool:
    if not secret:
        return False
    parts = [part.strip().partition("=")[::2] for part in header.split(",")]
    timestamps = [v for k, v in parts if k == "t"]
    if len(timestamps) != 1:
        return False
    timestamp = timestamps[0]
    try:
        if abs((time.time() if now is None else now) - int(timestamp)) > 300:
            return False
    except ValueError:
        return False
    expected = hmac.new(secret.encode(), timestamp.encode() + b"." + body, hashlib.sha256).hexdigest()
    return any(k == "v1" and len(v) == 64 and v.isascii() and hmac.compare_digest(expected, v) for k, v in parts)

class AsyncClient:
    def __init__(self, base_url: str, api_key: str, lane: str = "api_batch", timeout: float = 60):
        safe_url(base_url)
        self.base_url, self.api_key, self.lane, self.timeout = base_url.rstrip("/"), api_key, lane, timeout

    def input(self, upload_ids: list[str], rules_text: str = "") -> dict:
        return {"sources": [{"upload_id": i} for i in upload_ids], "rules_text": rules_text,
                "options": {"lane": self.lane, "use_rules": True, "web_search": False}}

    async def _response(self, method: str, path: str, body=None) -> httpx.Response:
        if not path.startswith("/v1/") or "\\" in path:
            raise ValueError("Expected relative API path")
        headers = {"Authorization": "Bearer " + self.api_key, "Accept": "application/json"}
        if method != "GET":
            headers["Idempotency-Key"] = str(uuid.uuid4())
        retry = method == "GET" or (method == "POST" and path == OPERATIONS["createRun"]["path"])
        async with httpx.AsyncClient(timeout=self.timeout, follow_redirects=False) as client:
            for attempt in range(4):
                try:
                    response = await client.request(method, self.base_url + path, headers=headers, **({"json": body} if body is not None else {}))
                except httpx.TransportError:
                    if retry and attempt < 3:
                        await asyncio.sleep(retry_delay(None, attempt))
                        continue
                    raise
                if 200 <= response.status_code < 300:
                    return response
                if attempt < 3 and (response.status_code == 429 or (retry and response.status_code >= 500)):
                    await asyncio.sleep(retry_delay(response.headers.get("Retry-After"), attempt))
                    continue
                try:
                    error = response.json()
                except ValueError:
                    error = {}
                raise DocmatchError(response.status_code, error.get("code", "http_error"), error.get("message", "Falha na API."), error.get("request_id"))
        raise RuntimeError("Unreachable retry state")

    async def request(self, method: str, path: str, body=None):
        response = await self._response(method, path, body)
        return None if response.status_code == 204 else response.json()

    async def estimate(self, input: dict) -> dict:
        return await self.request("POST", OPERATIONS["estimateRun"]["path"], input)

    async def run(self, quote: dict, input: dict) -> dict:
        return await self.request("POST", OPERATIONS["createRun"]["path"], {**input, "quote_id": quote["id"], "quote_signature": quote["signature"]})

    async def get_run(self, run_id: str) -> dict:
        return await self.request("GET", route("getRun", run_id))

    async def wait(self, run_id: str, timeout: float = 600, poll_interval: float = 1, on_progress=None) -> dict:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            run = await self.get_run(run_id)
            if on_progress:
                on_progress(run)
            if run["status"] == "completed":
                return run
            if run["status"] in ("failed", "cancelled", "needs_approval"):
                error = run.get("error") or {}
                raise DocmatchError(409, error.get("code", run["status"]), error.get("message", "Execução interrompida ou aguardando aprovação."))
            await asyncio.sleep(poll_interval)
        raise TimeoutError("Run wait timed out")

    async def findings(self, run_id: str) -> list[dict]:
        result, cursor = [], ""
        for _ in range(1000):
            page = await self.request("GET", route("listFindings", run_id) + "?limit=100&cursor=" + quote(cursor, safe=""))
            result.extend(page["items"])
            next_cursor = page.get("next_cursor")
            if not next_cursor:
                return result
            if cursor == next_cursor:
                raise RuntimeError("Repeated pagination cursor")
            cursor = next_cursor
        raise RuntimeError("Pagination limit exceeded")

    async def export(self, run_id: str, format: str = "xlsx") -> bytes:
        if format not in ("xlsx", "csv", "pdf"):
            raise ValueError("Invalid export format")
        return (await self._response("GET", route("exportRun", run_id) + "?format=" + format)).content

    async def upload(self, name: str, data: bytes, mime: str = "application/octet-stream") -> dict:
        digest = hashlib.sha256(data).hexdigest()
        ticket = await self.request("POST", OPERATIONS["createUpload"]["path"], {"kind": "document", "file_name": name, "size_bytes": len(data), "mime": mime, "sha256": digest})
        safe_url(ticket["url"])
        # Separate client: no API Authorization or cookies reach object storage.
        async with httpx.AsyncClient(timeout=120, follow_redirects=False) as storage:
            if ticket["method"] == "POST":
                response = await storage.post(ticket["url"], data=ticket.get("fields", {}), files={"file": (name, data, mime)})
            elif ticket["method"] == "PUT":
                response = await storage.put(ticket["url"], content=data, headers=ticket.get("headers", {}))
            else:
                raise ValueError("Unsupported upload method")
            if not 200 <= response.status_code < 300:
                raise DocmatchError(response.status_code, "upload_failed", "Falha no envio do arquivo.")
        for _ in range(120):
            upload = await self.request("POST", route("completeUpload", ticket["id"]), {"sha256": digest})
            if upload["status"] == "ready":
                return upload
            if upload["status"] not in ("pending", "scanning"):
                raise DocmatchError(422, "upload_rejected", upload.get("rejected_reason", upload["status"]))
            await asyncio.sleep(1)
        raise TimeoutError("Upload scan timeout")


class Client:
    def __init__(self, base_url: str, api_key: str, lane: str = "api_batch", timeout: float = 60):
        safe_url(base_url)
        self.base_url, self.api_key, self.lane, self.timeout = base_url.rstrip("/"), api_key, lane, timeout

    def input(self, upload_ids: list[str], rules_text: str = "") -> dict:
        return {"sources": [{"upload_id": i} for i in upload_ids], "rules_text": rules_text,
                "options": {"lane": self.lane, "use_rules": True, "web_search": False}}

    def _response(self, method: str, path: str, body=None) -> httpx.Response:
        if not path.startswith("/v1/") or "\\" in path:
            raise ValueError("Expected relative API path")
        headers = {"Authorization": "Bearer " + self.api_key, "Accept": "application/json"}
        if method != "GET":
            headers["Idempotency-Key"] = str(uuid.uuid4())
        retry = method == "GET" or (method == "POST" and path == OPERATIONS["createRun"]["path"])
        with httpx.Client(timeout=self.timeout, follow_redirects=False) as client:
            for attempt in range(4):
                try:
                    response = client.request(method, self.base_url + path, headers=headers, **({"json": body} if body is not None else {}))
                except httpx.TransportError:
                    if retry and attempt < 3:
                        time.sleep(retry_delay(None, attempt))
                        continue
                    raise
                if 200 <= response.status_code < 300:
                    return response
                if attempt < 3 and (response.status_code == 429 or (retry and response.status_code >= 500)):
                    time.sleep(retry_delay(response.headers.get("Retry-After"), attempt))
                    continue
                try:
                    error = response.json()
                except ValueError:
                    error = {}
                raise DocmatchError(response.status_code, error.get("code", "http_error"), error.get("message", "Falha na API."), error.get("request_id"))
        raise RuntimeError("Unreachable retry state")

    def request(self, method: str, path: str, body=None):
        response = self._response(method, path, body)
        return None if response.status_code == 204 else response.json()

    def estimate(self, input: dict) -> dict:
        return self.request("POST", OPERATIONS["estimateRun"]["path"], input)

    def run(self, quote: dict, input: dict) -> dict:
        return self.request("POST", OPERATIONS["createRun"]["path"], {**input, "quote_id": quote["id"], "quote_signature": quote["signature"]})

    def get_run(self, run_id: str) -> dict:
        return self.request("GET", route("getRun", run_id))

    def wait(self, run_id: str, timeout: float = 600, poll_interval: float = 1, on_progress=None) -> dict:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            run = self.get_run(run_id)
            if on_progress:
                on_progress(run)
            if run["status"] == "completed":
                return run
            if run["status"] in ("failed", "cancelled", "needs_approval"):
                error = run.get("error") or {}
                raise DocmatchError(409, error.get("code", run["status"]), error.get("message", "Execução interrompida ou aguardando aprovação."))
            time.sleep(poll_interval)
        raise TimeoutError("Run wait timed out")

    def findings(self, run_id: str) -> list[dict]:
        result, cursor = [], ""
        for _ in range(1000):
            page = self.request("GET", route("listFindings", run_id) + "?limit=100&cursor=" + quote(cursor, safe=""))
            result.extend(page["items"])
            next_cursor = page.get("next_cursor")
            if not next_cursor:
                return result
            if cursor == next_cursor:
                raise RuntimeError("Repeated pagination cursor")
            cursor = next_cursor
        raise RuntimeError("Pagination limit exceeded")

    def export(self, run_id: str, format: str = "xlsx") -> bytes:
        if format not in ("xlsx", "csv", "pdf"):
            raise ValueError("Invalid export format")
        return (self._response("GET", route("exportRun", run_id) + "?format=" + format)).content

    def upload(self, name: str, data: bytes, mime: str = "application/octet-stream") -> dict:
        digest = hashlib.sha256(data).hexdigest()
        ticket = self.request("POST", OPERATIONS["createUpload"]["path"], {"kind": "document", "file_name": name, "size_bytes": len(data), "mime": mime, "sha256": digest})
        safe_url(ticket["url"])
        # Separate client: no API Authorization or cookies reach object storage.
        with httpx.Client(timeout=120, follow_redirects=False) as storage:
            if ticket["method"] == "POST":
                response = storage.post(ticket["url"], data=ticket.get("fields", {}), files={"file": (name, data, mime)})
            elif ticket["method"] == "PUT":
                response = storage.put(ticket["url"], content=data, headers=ticket.get("headers", {}))
            else:
                raise ValueError("Unsupported upload method")
            if not 200 <= response.status_code < 300:
                raise DocmatchError(response.status_code, "upload_failed", "Falha no envio do arquivo.")
        for _ in range(120):
            upload = self.request("POST", route("completeUpload", ticket["id"]), {"sha256": digest})
            if upload["status"] == "ready":
                return upload
            if upload["status"] not in ("pending", "scanning"):
                raise DocmatchError(422, "upload_rejected", upload.get("rejected_reason", upload["status"]))
            time.sleep(1)
        raise TimeoutError("Upload scan timeout")
