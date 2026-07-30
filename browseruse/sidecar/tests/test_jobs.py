from __future__ import annotations

import asyncio
import json
import unittest
from pathlib import Path
from tempfile import TemporaryDirectory

from fastapi import HTTPException

from app.artifacts import BrowserArtifacts
from app.config import Settings
from app.jobs import JobManager
from app.models import JobResult, JobStatus, StartJobRequest


def settings() -> Settings:
	return Settings(
		service_token="test-token",
		data_dir=Path("/tmp/browser-use-tests"),
		executable_path="/usr/bin/chromium",
		llm_provider="openai",
		llm_model="test-model",
		llm_base_url=None,
		max_concurrent_jobs=2,
		max_steps=20,
		job_timeout_seconds=30,
		job_ttl_seconds=60,
		max_jobs=20,
		headless=True,
		chromium_sandbox=False,
		block_ip_addresses=True,
		default_use_vision=False,
		allowed_domains=(),
		prohibited_domains=(),
	)


def result() -> JobResult:
	return JobResult(
		final_result="done",
		done=True,
		successful=True,
		steps=1,
		duration_seconds=0.01,
	)


class CompletingRunner:
	async def run(self, _session_id: str, _job_id: str, _request: StartJobRequest) -> JobResult:
		await asyncio.sleep(0)
		return result()

	def screenshot_available(self, _job_id: str) -> bool:
		return True

	def read_screenshot(self, _job_id: str) -> bytes:
		return b"PNG"

	def network_loads(self, _job_id: str, _limit: int):
		return 1, []


class BlockingRunner:
	def __init__(self):
		self.started = asyncio.Event()

	async def run(self, _session_id: str, _job_id: str, _request: StartJobRequest) -> JobResult:
		self.started.set()
		await asyncio.Event().wait()
		return result()


class JobManagerTests(unittest.IsolatedAsyncioTestCase):
	async def test_wait_follows_running_job_until_terminal(self):
		manager = JobManager(settings(), CompletingRunner())
		created = await manager.create("session-1", StartJobRequest(task="test"))
		completed = await manager.get("session-1", created.id, wait_seconds=2)
		self.assertEqual(completed.status, JobStatus.SUCCEEDED)
		self.assertEqual(completed.result, result())
		await manager.shutdown()

	async def test_job_is_hidden_from_other_sessions(self):
		manager = JobManager(settings(), CompletingRunner())
		created = await manager.create("session-1", StartJobRequest(task="test"))
		with self.assertRaises(HTTPException) as caught:
			await manager.get("session-2", created.id)
		self.assertEqual(caught.exception.status_code, 404)
		await manager.shutdown()

	async def test_screenshot_is_scoped_to_job_owner(self):
		manager = JobManager(settings(), CompletingRunner())
		created = await manager.create("session-1", StartJobRequest(task="test"))
		await manager.get("session-1", created.id, wait_seconds=2)
		self.assertEqual(await manager.screenshot("session-1", created.id), b"PNG")
		with self.assertRaises(HTTPException) as caught:
			await manager.screenshot("session-2", created.id)
		self.assertEqual(caught.exception.status_code, 404)
		await manager.shutdown()

	async def test_debug_snapshot_includes_recent_jobs_and_load_counts(self):
		manager = JobManager(settings(), CompletingRunner())
		created = await manager.create("session-1", StartJobRequest(task="inspect example.com"))
		await manager.get("session-1", created.id, wait_seconds=2)
		snapshot = await manager.debug(job_limit=10, load_limit=50)
		self.assertEqual(snapshot.total_jobs, 1)
		self.assertEqual(snapshot.jobs[0].session_id, "session-1")
		self.assertEqual(snapshot.jobs[0].load_count, 1)
		self.assertTrue(snapshot.jobs[0].screenshot_available)
		await manager.shutdown()

	async def test_immediate_cancel_transitions_queued_job(self):
		manager = JobManager(settings(), BlockingRunner())
		created = await manager.create("session-1", StartJobRequest(task="test"))
		cancelled = await manager.cancel("session-1", created.id)
		self.assertEqual(cancelled.status, JobStatus.CANCELLED)
		await manager.shutdown()

	async def test_cancel_running_job(self):
		runner = BlockingRunner()
		manager = JobManager(settings(), runner)
		created = await manager.create("session-1", StartJobRequest(task="test"))
		await asyncio.wait_for(runner.started.wait(), timeout=1)
		cancelled = await manager.cancel("session-1", created.id)
		self.assertEqual(cancelled.status, JobStatus.CANCELLED)
		await manager.shutdown()


class BrowserArtifactsTests(unittest.TestCase):
	def test_har_is_projected_to_bounded_non_sensitive_load_metadata(self):
		with TemporaryDirectory() as directory:
			artifacts = BrowserArtifacts(Path(directory))
			har_path, _ = artifacts.prepare("job-1")
			har_path.write_text(
				json.dumps(
					{
						"log": {
							"entries": [
								{
									"startedDateTime": "2026-07-30T10:00:00Z",
									"time": 12.5,
									"request": {
										"method": "GET",
										"url": "https://example.com/app.js",
										"headers": [{"name": "Authorization", "value": "secret"}],
									},
									"response": {
										"status": 200,
										"statusText": "OK",
										"bodySize": 42,
										"content": {"mimeType": "text/javascript", "text": "private body"},
									},
								}
							]
						}
					}
				),
				encoding="utf-8",
			)

			count, loads = artifacts.network_loads("job-1", 10)
			self.assertEqual(count, 1)
			self.assertEqual(loads[0].url, "https://example.com/app.js")
			self.assertEqual(loads[0].bytes, 42)
			self.assertNotIn("secret", loads[0].model_dump_json())
			self.assertNotIn("private body", loads[0].model_dump_json())
			artifacts.sanitize_har("job-1")
			sanitized = har_path.read_text(encoding="utf-8")
			self.assertNotIn("Authorization", sanitized)
			self.assertNotIn("secret", sanitized)
			self.assertNotIn("private body", sanitized)

	def test_screenshot_round_trip_is_atomic_and_bounded(self):
		with TemporaryDirectory() as directory:
			artifacts = BrowserArtifacts(Path(directory))
			artifacts.save_screenshot("job-1", b"PNG")
			self.assertTrue(artifacts.screenshot_available("job-1"))
			self.assertEqual(artifacts.read_screenshot("job-1"), b"PNG")
