from __future__ import annotations

import asyncio
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from uuid import uuid4

from fastapi import HTTPException, status

from .config import Settings
from .models import JobResponse, JobResult, JobStatus, StartJobRequest
from .runner import BrowserUseRunner


@dataclass
class _Job:
	id: str
	session_id: str
	request: StartJobRequest
	status: JobStatus = JobStatus.QUEUED
	created_at: datetime = field(default_factory=lambda: datetime.now(UTC))
	started_at: datetime | None = None
	completed_at: datetime | None = None
	result: JobResult | None = None
	error: str = ""
	task: asyncio.Task[None] | None = None
	changed: asyncio.Condition = field(default_factory=asyncio.Condition)

	def response(self) -> JobResponse:
		return JobResponse(
			id=self.id,
			status=self.status,
			created_at=self.created_at,
			started_at=self.started_at,
			completed_at=self.completed_at,
			result=self.result,
			error=self.error,
		)


class JobManager:
	def __init__(self, settings: Settings, runner: BrowserUseRunner):
		self.settings = settings
		self.runner = runner
		self._jobs: dict[str, _Job] = {}
		self._jobs_lock = asyncio.Lock()
		self._semaphore = asyncio.Semaphore(settings.max_concurrent_jobs)
		self._session_locks: dict[str, asyncio.Lock] = {}

	async def create(self, session_id: str, request: StartJobRequest) -> JobResponse:
		async with self._jobs_lock:
			self._prune_locked()
			if len(self._jobs) >= self.settings.max_jobs:
				raise HTTPException(
					status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
					detail="browser job capacity reached; retry after completed jobs expire",
				)
			job = _Job(id=str(uuid4()), session_id=session_id, request=request)
			self._jobs[job.id] = job
			self._session_locks.setdefault(session_id, asyncio.Lock())
			job.task = asyncio.create_task(self._execute(job), name=f"browser-use:{job.id}")
			return job.response()

	async def get(self, session_id: str, job_id: str, wait_seconds: float = 0) -> JobResponse:
		job = await self._owned_job(session_id, job_id)
		if wait_seconds > 0 and not job.status.terminal:
			deadline = asyncio.get_running_loop().time() + wait_seconds
			async with job.changed:
				while not job.status.terminal:
					remaining = deadline - asyncio.get_running_loop().time()
					if remaining <= 0:
						break
					try:
						await asyncio.wait_for(job.changed.wait(), timeout=remaining)
					except TimeoutError:
						break
		return job.response()

	async def cancel(self, session_id: str, job_id: str) -> JobResponse:
		job = await self._owned_job(session_id, job_id)
		task = job.task
		if not job.status.terminal and task is not None:
			task.cancel()
			try:
				await task
			except asyncio.CancelledError:
				pass
			# A task cancelled before its coroutine gets its first event-loop turn
			# never reaches _execute's CancelledError handler.
			if not job.status.terminal:
				await self._transition(job, JobStatus.CANCELLED)
		return job.response()

	async def shutdown(self) -> None:
		async with self._jobs_lock:
			tasks = [job.task for job in self._jobs.values() if job.task and not job.task.done()]
		for task in tasks:
			task.cancel()
		if tasks:
			await asyncio.gather(*tasks, return_exceptions=True)

	async def _execute(self, job: _Job) -> None:
		try:
			async with self._semaphore:
				session_lock = self._session_locks[job.session_id]
				async with session_lock:
					await self._transition(job, JobStatus.RUNNING)
					job.result = await asyncio.wait_for(
						self.runner.run(job.session_id, job.id, job.request),
						timeout=self.settings.job_timeout_seconds,
					)
					await self._transition(job, JobStatus.SUCCEEDED)
		except asyncio.CancelledError:
			await self._transition(job, JobStatus.CANCELLED)
			raise
		except TimeoutError:
			job.error = f"browser job exceeded {self.settings.job_timeout_seconds} second timeout"
			await self._transition(job, JobStatus.FAILED)
		except Exception as exc:
			job.error = _safe_error(exc)
			await self._transition(job, JobStatus.FAILED)

	async def _transition(self, job: _Job, new_status: JobStatus) -> None:
		async with job.changed:
			job.status = new_status
			now = datetime.now(UTC)
			if new_status == JobStatus.RUNNING:
				job.started_at = now
			if new_status.terminal:
				job.completed_at = now
			job.changed.notify_all()

	async def _owned_job(self, session_id: str, job_id: str) -> _Job:
		async with self._jobs_lock:
			job = self._jobs.get(job_id)
		if job is None:
			raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="browser job not found")
		if job.session_id != session_id:
			# Do not reveal whether another session owns the requested identifier.
			raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="browser job not found")
		return job

	def _prune_locked(self) -> None:
		cutoff = datetime.now(UTC) - timedelta(seconds=self.settings.job_ttl_seconds)
		expired = [
			job_id
			for job_id, job in self._jobs.items()
			if job.status.terminal and job.completed_at is not None and job.completed_at < cutoff
		]
		for job_id in expired:
			del self._jobs[job_id]
		active_sessions = {job.session_id for job in self._jobs.values()}
		for session_id in list(self._session_locks):
			if session_id not in active_sessions:
				del self._session_locks[session_id]


def _safe_error(error: Exception) -> str:
	message = f"{type(error).__name__}: {error}".strip()
	if len(message) > 4_000:
		return message[:4_000] + "..."
	return message
