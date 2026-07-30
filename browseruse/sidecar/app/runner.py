from __future__ import annotations

import hashlib
import os
from pathlib import Path
from typing import Any

from browser_use import Agent, BrowserProfile, BrowserSession

from .artifacts import BrowserArtifacts
from .config import Settings
from .models import JobResult, StartJobRequest


class BrowserUseRunner:
	def __init__(self, settings: Settings):
		self.settings = settings
		self.llm = self._create_llm()
		self.artifacts = BrowserArtifacts(settings.data_dir)

	async def run(self, session_id: str, job_id: str, request: StartJobRequest) -> JobResult:
		session_key = hashlib.sha256(session_id.encode("utf-8")).hexdigest()
		profile_dir = self.settings.data_dir / "profiles" / session_key
		downloads_dir = self.settings.data_dir / "downloads" / job_id
		profile_dir.mkdir(parents=True, exist_ok=True)
		downloads_dir.mkdir(parents=True, exist_ok=True)
		har_path, _ = self.artifacts.prepare(job_id)

		profile = BrowserProfile(
			executable_path=self.settings.executable_path,
			headless=self.settings.headless,
			user_data_dir=profile_dir,
			downloads_path=downloads_dir,
			allowed_domains=self._allowed_domains(request),
			prohibited_domains=list(self.settings.prohibited_domains) or None,
			block_ip_addresses=self.settings.block_ip_addresses,
			chromium_sandbox=self.settings.chromium_sandbox,
			keep_alive=False,
			proxy={"server": self.settings.proxy_url} if self.settings.proxy_url else None,
			record_har_path=har_path,
			record_har_content="omit",
			record_har_mode="full",
		)
		browser = BrowserSession(browser_profile=profile)
		agent = Agent(
			task=request.task,
			llm=self.llm,
			browser=browser,
			use_vision=self.settings.default_use_vision if request.use_vision is None else request.use_vision,
			use_judge=False,
			enable_signal_handler=False,
			calculate_cost=True,
		)

		async def capture_latest_screenshot(_: Agent) -> None:
			try:
				data = await browser.take_screenshot(full_page=False, format="png")
				self.artifacts.save_screenshot(job_id, data)
			except Exception:
				# Screenshot capture is best-effort and must never fail the browser task.
				pass

		try:
			history = await agent.run(
				max_steps=request.max_steps or self.settings.max_steps,
				on_step_end=capture_latest_screenshot,
			)
		finally:
			# browser-use's HAR writer includes headers even when response bodies
			# are omitted. Strip sensitive fields before the artifact remains at rest.
			self.artifacts.sanitize_har(job_id)
		return JobResult(
			final_result=_truncate(history.final_result() or "", 64_000),
			done=history.is_done(),
			successful=history.is_successful(),
			visited_urls=_unique_strings(history.urls(), 200, 2_000),
			steps=history.number_of_steps(),
			duration_seconds=history.total_duration_seconds(),
			action_names=_unique_strings(history.action_names(), 500, 200),
			actions=_bounded_actions(history.action_history()),
			errors=_unique_strings(history.errors(), 100, 4_000),
		)

	def screenshot_available(self, job_id: str) -> bool:
		return self.artifacts.screenshot_available(job_id)

	def read_screenshot(self, job_id: str) -> bytes:
		return self.artifacts.read_screenshot(job_id)

	def network_loads(self, job_id: str, limit: int):
		return self.artifacts.network_loads(job_id, limit)

	def cleanup(self, job_id: str) -> None:
		self.artifacts.cleanup(job_id)

	def _allowed_domains(self, request: StartJobRequest) -> list[str] | None:
		# A deployment-wide allowlist is authoritative. Callers can only provide a
		# per-job allowlist when the operator has not configured one.
		if self.settings.allowed_domains:
			return list(self.settings.allowed_domains)
		return request.allowed_domains or None

	def _create_llm(self):
		provider = self.settings.llm_provider
		model = self.settings.llm_model
		base_url = self.settings.llm_base_url
		if provider == "openai":
			from browser_use import ChatOpenAI

			return ChatOpenAI(
				model=model,
				api_key=_required_key("OPENAI_API_KEY"),
				base_url=base_url,
			)
		if provider == "browser-use":
			from browser_use import ChatBrowserUse

			return ChatBrowserUse(
				model=model,
				api_key=_required_key("BROWSER_USE_API_KEY"),
			)
		if provider == "openrouter":
			from browser_use.llm.openrouter.chat import ChatOpenRouter

			return ChatOpenRouter(
				model=model,
				api_key=_required_key("OPENROUTER_API_KEY"),
				base_url=base_url or "https://openrouter.ai/api/v1",
			)
		if provider == "anthropic":
			from browser_use import ChatAnthropic

			return ChatAnthropic(
				model=model,
				api_key=_required_key("ANTHROPIC_API_KEY"),
				base_url=base_url,
			)
		if provider == "google":
			from browser_use import ChatGoogle

			return ChatGoogle(
				model=model,
				api_key=_required_key("GOOGLE_API_KEY"),
			)
		raise RuntimeError(f"unsupported LLM provider: {provider}")


def _truncate(value: str, limit: int) -> str:
	return value if len(value) <= limit else value[:limit] + "..."


def _required_key(name: str) -> str:
	value = os.getenv(name, "").strip()
	if not value:
		raise RuntimeError(f"{name} is required for the selected browser-use LLM provider")
	return value


def _unique_strings(values: list[Any], max_items: int, max_length: int) -> list[str]:
	result: list[str] = []
	for value in values:
		if value is None:
			continue
		text = _truncate(str(value), max_length)
		if text and text not in result:
			result.append(text)
		if len(result) >= max_items:
			break
	return result


def _bounded_actions(steps: list[list[dict[str, Any]]]) -> list[dict[str, Any]]:
	actions: list[dict[str, Any]] = []
	for step_number, step in enumerate(steps, start=1):
		for action in step:
			item = _sanitize(action, depth=0)
			if isinstance(item, dict):
				item["step"] = step_number
				actions.append(item)
			if len(actions) >= 200:
				return actions
	return actions


def _sanitize(value: Any, depth: int) -> Any:
	if depth >= 5:
		return _truncate(str(value), 1_000)
	if isinstance(value, str):
		return _truncate(value, 4_000)
	if isinstance(value, dict):
		return {str(key)[:200]: _sanitize(item, depth + 1) for key, item in list(value.items())[:50]}
	if isinstance(value, list):
		return [_sanitize(item, depth + 1) for item in value[:50]]
	if value is None or isinstance(value, (bool, int, float)):
		return value
	return _truncate(str(value), 1_000)
