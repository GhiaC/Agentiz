from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path


def _required(name: str) -> str:
	value = os.getenv(name, "").strip()
	if not value:
		raise RuntimeError(f"{name} is required")
	return value


def _integer(name: str, default: int, minimum: int, maximum: int) -> int:
	raw = os.getenv(name, str(default)).strip()
	try:
		value = int(raw)
	except ValueError as exc:
		raise RuntimeError(f"{name} must be an integer") from exc
	if value < minimum or value > maximum:
		raise RuntimeError(f"{name} must be between {minimum} and {maximum}")
	return value


def _boolean(name: str, default: bool) -> bool:
	raw = os.getenv(name)
	if raw is None:
		return default
	value = raw.strip().lower()
	if value in {"1", "true", "yes", "on"}:
		return True
	if value in {"0", "false", "no", "off"}:
		return False
	raise RuntimeError(f"{name} must be a boolean")


def _csv(name: str) -> tuple[str, ...]:
	return tuple(item.strip() for item in os.getenv(name, "").split(",") if item.strip())


def _proxy_url() -> str | None:
	for name in ("BROWSER_USE_PROXY_URL", "http_proxy", "HTTP_PROXY"):
		value = os.getenv(name, "").strip()
		if value:
			return value
	return None


@dataclass(frozen=True)
class Settings:
	service_token: str
	data_dir: Path
	executable_path: str
	llm_provider: str
	llm_model: str
	llm_base_url: str | None
	max_concurrent_jobs: int
	max_steps: int
	job_timeout_seconds: int
	job_ttl_seconds: int
	max_jobs: int
	headless: bool
	chromium_sandbox: bool
	block_ip_addresses: bool
	default_use_vision: bool
	allowed_domains: tuple[str, ...]
	prohibited_domains: tuple[str, ...]
	proxy_url: str | None

	@classmethod
	def from_environment(cls) -> "Settings":
		provider = os.getenv("BROWSER_USE_LLM_PROVIDER", "openai").strip().lower()
		if provider not in {"openai", "browser-use", "openrouter", "anthropic", "google"}:
			raise RuntimeError(
				"BROWSER_USE_LLM_PROVIDER must be openai, browser-use, openrouter, anthropic, or google"
			)
		model = os.getenv("BROWSER_USE_LLM_MODEL", "gpt-5-mini").strip()
		if not model:
			raise RuntimeError("BROWSER_USE_LLM_MODEL cannot be blank")
		return cls(
			service_token=_required("BROWSER_USE_SIDECAR_TOKEN"),
			data_dir=Path(os.getenv("BROWSER_USE_DATA_DIR", "/data")).resolve(),
			executable_path=os.getenv("BROWSER_USE_EXECUTABLE_PATH", "/usr/bin/chromium").strip(),
			llm_provider=provider,
			llm_model=model,
			llm_base_url=os.getenv("BROWSER_USE_LLM_BASE_URL", "").strip() or None,
			max_concurrent_jobs=_integer("BROWSER_USE_MAX_CONCURRENT_JOBS", 2, 1, 16),
			max_steps=_integer("BROWSER_USE_MAX_STEPS", 50, 1, 500),
			job_timeout_seconds=_integer("BROWSER_USE_JOB_TIMEOUT_SECONDS", 600, 30, 7200),
			job_ttl_seconds=_integer("BROWSER_USE_JOB_TTL_SECONDS", 3600, 60, 86400),
			max_jobs=_integer("BROWSER_USE_MAX_JOBS", 1000, 10, 10000),
			headless=_boolean("BROWSER_USE_HEADLESS", True),
			chromium_sandbox=_boolean("BROWSER_USE_CHROMIUM_SANDBOX", False),
			block_ip_addresses=_boolean("BROWSER_USE_BLOCK_IP_ADDRESSES", True),
			default_use_vision=_boolean("BROWSER_USE_DEFAULT_USE_VISION", True),
			allowed_domains=_csv("BROWSER_USE_ALLOWED_DOMAINS"),
			prohibited_domains=_csv("BROWSER_USE_PROHIBITED_DOMAINS"),
			proxy_url=_proxy_url(),
		)
