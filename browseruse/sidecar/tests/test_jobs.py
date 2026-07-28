from __future__ import annotations

import asyncio
import unittest
from pathlib import Path

from fastapi import HTTPException

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
