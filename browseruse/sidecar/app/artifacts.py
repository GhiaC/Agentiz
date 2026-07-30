from __future__ import annotations

import json
import shutil
from datetime import datetime
from pathlib import Path
from typing import Any

from .models import BrowserLoad


MAX_SCREENSHOT_BYTES = 10 << 20


class BrowserArtifacts:
	"""Owns bounded, non-sensitive browser debug artifacts for one sidecar."""

	def __init__(self, data_dir: Path):
		self._root = (data_dir / "debug").resolve()
		self._network_dir = self._root / "network"
		self._screenshot_dir = self._root / "screenshots"

	def har_path(self, job_id: str) -> Path:
		return self._network_dir / f"{job_id}.har"

	def screenshot_path(self, job_id: str) -> Path:
		return self._screenshot_dir / job_id / "latest.png"

	def prepare(self, job_id: str) -> tuple[Path, Path]:
		har_path = self.har_path(job_id)
		screenshot_path = self.screenshot_path(job_id)
		har_path.parent.mkdir(parents=True, exist_ok=True)
		screenshot_path.parent.mkdir(parents=True, exist_ok=True)
		return har_path, screenshot_path

	def save_screenshot(self, job_id: str, data: bytes) -> None:
		if not data:
			raise ValueError("screenshot is empty")
		if len(data) > MAX_SCREENSHOT_BYTES:
			raise ValueError(f"screenshot exceeds {MAX_SCREENSHOT_BYTES} byte limit")
		path = self.screenshot_path(job_id)
		path.parent.mkdir(parents=True, exist_ok=True)
		temporary = path.with_suffix(".tmp")
		temporary.write_bytes(data)
		temporary.replace(path)

	def screenshot_available(self, job_id: str) -> bool:
		path = self.screenshot_path(job_id)
		try:
			return path.is_file() and 0 < path.stat().st_size <= MAX_SCREENSHOT_BYTES
		except OSError:
			return False

	def read_screenshot(self, job_id: str) -> bytes:
		path = self.screenshot_path(job_id)
		data = path.read_bytes()
		if not data:
			raise FileNotFoundError("browser screenshot is empty")
		if len(data) > MAX_SCREENSHOT_BYTES:
			raise ValueError(f"browser screenshot exceeds {MAX_SCREENSHOT_BYTES} byte limit")
		return data

	def network_loads(self, job_id: str, limit: int) -> tuple[int, list[BrowserLoad]]:
		"""Read request metadata from HAR; bodies and headers are never returned."""
		path = self.har_path(job_id)
		try:
			payload = json.loads(path.read_text(encoding="utf-8"))
		except (FileNotFoundError, OSError, UnicodeDecodeError, json.JSONDecodeError):
			return 0, []

		entries = payload.get("log", {}).get("entries", [])
		if not isinstance(entries, list):
			return 0, []
		total = len(entries)
		bounded = entries[-max(0, limit) :] if limit > 0 else []
		return total, [_load_from_har(item) for item in bounded if isinstance(item, dict)]

	def sanitize_har(self, job_id: str) -> None:
		"""Remove headers, cookies, and bodies before the debug HAR remains at rest."""
		path = self.har_path(job_id)
		try:
			payload = json.loads(path.read_text(encoding="utf-8"))
		except (FileNotFoundError, OSError, UnicodeDecodeError, json.JSONDecodeError):
			return
		entries = payload.get("log", {}).get("entries", [])
		if not isinstance(entries, list):
			return
		for entry in entries:
			if not isinstance(entry, dict):
				continue
			request = entry.get("request")
			if isinstance(request, dict):
				request["headers"] = []
				request["cookies"] = []
				request["postData"] = None
			response = entry.get("response")
			if isinstance(response, dict):
				response["headers"] = []
				response["cookies"] = []
				content = response.get("content")
				if isinstance(content, dict):
					for key in ("text", "encoding", "_file"):
						content.pop(key, None)
		temporary = path.with_suffix(".sanitized.tmp")
		try:
			temporary.write_text(json.dumps(payload, separators=(",", ":")), encoding="utf-8")
			temporary.replace(path)
		except OSError:
			temporary.unlink(missing_ok=True)

	def cleanup(self, job_id: str) -> None:
		try:
			self.har_path(job_id).unlink(missing_ok=True)
		except OSError:
			pass
		try:
			shutil.rmtree(self.screenshot_path(job_id).parent)
		except FileNotFoundError:
			pass
		except OSError:
			pass


def _load_from_har(entry: dict[str, Any]) -> BrowserLoad:
	request = entry.get("request") if isinstance(entry.get("request"), dict) else {}
	response = entry.get("response") if isinstance(entry.get("response"), dict) else {}
	content = response.get("content") if isinstance(response.get("content"), dict) else {}

	status = _integer(response.get("status"))
	body_size = _integer(response.get("bodySize"))
	started_at = _datetime(entry.get("startedDateTime"))
	url = _bounded_text(request.get("url"), 2_000)
	return BrowserLoad(
		started_at=started_at,
		duration_ms=max(0.0, _number(entry.get("time"))),
		method=_bounded_text(request.get("method"), 16) or "GET",
		url=url,
		status=max(0, status),
		status_text=_bounded_text(response.get("statusText"), 120),
		mime_type=_bounded_text(content.get("mimeType"), 255),
		bytes=max(0, body_size),
		failed=status <= 0,
	)


def _bounded_text(value: Any, limit: int) -> str:
	text = "" if value is None else str(value)
	return text if len(text) <= limit else text[:limit] + "..."


def _integer(value: Any) -> int:
	try:
		return int(value)
	except (TypeError, ValueError):
		return 0


def _number(value: Any) -> float:
	try:
		return float(value)
	except (TypeError, ValueError):
		return 0.0


def _datetime(value: Any) -> datetime | None:
	if not isinstance(value, str) or not value:
		return None
	try:
		return datetime.fromisoformat(value.replace("Z", "+00:00"))
	except ValueError:
		return None
