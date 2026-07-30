from __future__ import annotations

from datetime import datetime
from enum import StrEnum
from typing import Any

from pydantic import BaseModel, ConfigDict, Field, field_validator


class JobStatus(StrEnum):
	QUEUED = "queued"
	RUNNING = "running"
	SUCCEEDED = "succeeded"
	FAILED = "failed"
	CANCELLED = "cancelled"

	@property
	def terminal(self) -> bool:
		return self in {self.SUCCEEDED, self.FAILED, self.CANCELLED}


class StartJobRequest(BaseModel):
	model_config = ConfigDict(extra="forbid")

	task: str = Field(min_length=1, max_length=20_000)
	allowed_domains: list[str] = Field(default_factory=list, max_length=100)
	max_steps: int | None = Field(default=None, ge=1, le=500)
	use_vision: bool | None = None

	@field_validator("task")
	@classmethod
	def normalize_task(cls, value: str) -> str:
		value = value.strip()
		if not value:
			raise ValueError("task cannot be blank")
		return value

	@field_validator("allowed_domains")
	@classmethod
	def normalize_domains(cls, values: list[str]) -> list[str]:
		result: list[str] = []
		for value in values:
			value = value.strip()
			if not value or len(value) > 255:
				raise ValueError("allowed domain entries must be 1-255 characters")
			if value not in result:
				result.append(value)
		return result


class JobResult(BaseModel):
	final_result: str = ""
	done: bool
	successful: bool | None = None
	visited_urls: list[str] = Field(default_factory=list)
	steps: int
	duration_seconds: float
	action_names: list[str] = Field(default_factory=list)
	actions: list[dict[str, Any]] = Field(default_factory=list)
	errors: list[str] = Field(default_factory=list)


class JobResponse(BaseModel):
	id: str
	status: JobStatus
	created_at: datetime
	started_at: datetime | None = None
	completed_at: datetime | None = None
	result: JobResult | None = None
	error: str = ""
	screenshot_available: bool = False


class BrowserLoad(BaseModel):
	started_at: datetime | None = None
	duration_ms: float = 0
	method: str = "GET"
	url: str = ""
	status: int = 0
	status_text: str = ""
	mime_type: str = ""
	bytes: int = 0
	failed: bool = False


class DebugJobResponse(JobResponse):
	session_id: str
	task: str
	load_count: int = 0
	loads: list[BrowserLoad] = Field(default_factory=list)


class BrowserDebugResponse(BaseModel):
	total_jobs: int
	running_jobs: int
	max_jobs: int
	max_concurrent_jobs: int
	jobs: list[DebugJobResponse] = Field(default_factory=list)


class HealthResponse(BaseModel):
	status: str = "ok"
	component: str = "agentize-browser-use"
