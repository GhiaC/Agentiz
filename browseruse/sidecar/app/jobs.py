from __future__ import annotations

import asyncio
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from typing import Protocol
from uuid import uuid4

from fastapi import HTTPException, status

from .config import Settings
from .models import BrowserDebugResponse, BrowserDownload, DebugJobResponse, JobResponse, JobResult, JobStatus, StartJobRequest


class BrowserRunner(Protocol):
	async def run(self, session_id: str, job_id: str, request: StartJobRequest) -> JobResult: ...


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
	def __init__(self, settings: Settings, runner: BrowserRunner):
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
		return self._response(job)

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
		return self._response(job)

	async def screenshot(self, session_id: str, job_id: str) -> bytes:
		job = await self._owned_job(session_id, job_id)
		reader = getattr(self.runner, "read_screenshot", None)
		if reader is None:
			raise HTTPException(
				status_code=status.HTTP_501_NOT_IMPLEMENTED,
				detail="browser runner does not support screenshots",
			)
		try:
			return reader(job.id)
		except FileNotFoundError:
			raise HTTPException(
				status_code=status.HTTP_404_NOT_FOUND,
				detail="no screenshot is available for this browser job yet",
			) from None
		except ValueError as exc:
			raise HTTPException(status_code=status.HTTP_413_REQUEST_ENTITY_TOO_LARGE, detail=str(exc)) from exc

	async def downloads(self, session_id: str, job_id: str) -> list[BrowserDownload]:
		job = await self._owned_job(session_id, job_id)
		lister = getattr(self.runner, "list_downloads", None)
		if lister is None:
			raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail="browser runner does not support downloads")
		return lister(job.id)

	async def download(self, session_id: str, job_id: str, name: str) -> tuple[BrowserDownload, bytes]:
		job = await self._owned_job(session_id, job_id)
		reader = getattr(self.runner, "read_download", None)
		if reader is None:
			raise HTTPException(status_code=status.HTTP_501_NOT_IMPLEMENTED, detail="browser runner does not support downloads")
		try:
			return reader(job.id, name)
		except FileNotFoundError:
			raise HTTPException(status_code=status.HTTP_404_NOT_FOUND, detail="browser download not found") from None
		except ValueError as exc:
			raise HTTPException(status_code=status.HTTP_413_REQUEST_ENTITY_TOO_LARGE, detail=str(exc)) from exc

	async def debug(self, job_limit: int, load_limit: int) -> BrowserDebugResponse:
		async with self._jobs_lock:
			jobs = sorted(self._jobs.values(), key=lambda item: item.created_at, reverse=True)[:job_limit]
			total_jobs = len(self._jobs)
			running_jobs = sum(1 for item in self._jobs.values() if not item.status.terminal)

		debug_jobs: list[DebugJobResponse] = []
		load_reader = getattr(self.runner, "network_loads", None)
		for job in jobs:
			load_count, loads = load_reader(job.id, load_limit) if load_reader is not None else (0, [])
			response = self._response(job)
			debug_jobs.append(
				DebugJobResponse(
					**response.model_dump(),
					session_id=job.session_id,
					task=_truncate(job.request.task, 2_000),
					load_count=load_count,
					loads=loads,
				)
			)
		return BrowserDebugResponse(
			total_jobs=total_jobs,
			running_jobs=running_jobs,
			max_jobs=self.settings.max_jobs,
			max_concurrent_jobs=self.settings.max_concurrent_jobs,
			jobs=debug_jobs,
		)

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

	def _response(self, job: _Job) -> JobResponse:
		response = job.response()
		checker = getattr(self.runner, "screenshot_available", None)
		if checker is not None:
			response.screenshot_available = bool(checker(job.id))
		return response

	def _prune_locked(self) -> None:
		cutoff = datetime.now(UTC) - timedelta(seconds=self.settings.job_ttl_seconds)
		expired = [
			job_id
			for job_id, job in self._jobs.items()
			if job.status.terminal and job.completed_at is not None and job.completed_at < cutoff
		]
		for job_id in expired:
			del self._jobs[job_id]
			cleanup = getattr(self.runner, "cleanup", None)
			if cleanup is not None:
				cleanup(job_id)
		active_sessions = {job.session_id for job in self._jobs.values()}
		for session_id in list(self._session_locks):
			if session_id not in active_sessions:
				del self._session_locks[session_id]


def _safe_error(error: Exception) -> str:
	message = f"{type(error).__name__}: {error}".strip()
	if len(message) > 4_000:
		return message[:4_000] + "..."
	return message


def _truncate(value: str, limit: int) -> str:
	return value if len(value) <= limit else value[:limit] + "..."
