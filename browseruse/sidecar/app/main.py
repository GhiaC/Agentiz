from __future__ import annotations

import hmac
from contextlib import asynccontextmanager

from fastapi import Depends, FastAPI, Header, HTTPException, Query, Response, status
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer

from .config import Settings
from .jobs import JobManager
from .models import BrowserDebugResponse, BrowserDownloadsResponse, BrowserTabsResponse, HealthResponse, JobResponse, StartJobRequest
from .runner import BrowserUseRunner


settings = Settings.from_environment()
manager = JobManager(settings, BrowserUseRunner(settings))
bearer = HTTPBearer(auto_error=False)


@asynccontextmanager
async def lifespan(_: FastAPI):
	settings.data_dir.mkdir(parents=True, exist_ok=True)
	yield
	await manager.shutdown()


app = FastAPI(
	title="Agentize browser-use sidecar",
	version="1.0.0",
	docs_url=None,
	redoc_url=None,
	openapi_url=None,
	lifespan=lifespan,
)


def require_auth(credentials: HTTPAuthorizationCredentials | None = Depends(bearer)) -> None:
	if credentials is None or credentials.scheme.lower() != "bearer":
		raise HTTPException(status_code=status.HTTP_401_UNAUTHORIZED, detail="missing bearer token")
	if not hmac.compare_digest(credentials.credentials, settings.service_token):
		raise HTTPException(status_code=status.HTTP_401_UNAUTHORIZED, detail="invalid bearer token")


def require_session(
	session_id: str = Header(alias="X-Agentize-Session-ID", min_length=1, max_length=256),
) -> str:
	return session_id


@app.get("/health", response_model=HealthResponse)
async def health() -> HealthResponse:
	return HealthResponse()


@app.post(
	"/v1/jobs",
	response_model=JobResponse,
	status_code=status.HTTP_202_ACCEPTED,
	dependencies=[Depends(require_auth)],
)
async def create_job(
	request: StartJobRequest,
	session_id: str = Depends(require_session),
) -> JobResponse:
	return await manager.create(session_id, request)


@app.get(
	"/v1/debug/jobs",
	response_model=BrowserDebugResponse,
	dependencies=[Depends(require_auth)],
)
async def debug_jobs(
	limit: int = Query(default=20, ge=1, le=100),
	load_limit: int = Query(default=50, ge=0, le=250),
) -> BrowserDebugResponse:
	return await manager.debug(limit, load_limit)


@app.get(
	"/v1/jobs/{job_id}",
	response_model=JobResponse,
	dependencies=[Depends(require_auth)],
)
async def get_job(
	job_id: str,
	wait_seconds: float = Query(default=0, ge=0, le=60),
	session_id: str = Depends(require_session),
) -> JobResponse:
	return await manager.get(session_id, job_id, wait_seconds)


@app.post(
	"/v1/jobs/{job_id}/cancel",
	response_model=JobResponse,
	dependencies=[Depends(require_auth)],
)
async def cancel_job(
	job_id: str,
	session_id: str = Depends(require_session),
) -> JobResponse:
	return await manager.cancel(session_id, job_id)


@app.get(
	"/v1/jobs/{job_id}/screenshot",
	dependencies=[Depends(require_auth)],
	response_class=Response,
)
async def get_job_screenshot(
	job_id: str,
	session_id: str = Depends(require_session),
) -> Response:
	data = await manager.screenshot(session_id, job_id)
	return Response(
		content=data,
		media_type="image/png",
		headers={"Content-Disposition": f'inline; filename="browser-{job_id}.png"'},
	)


@app.get(
	"/v1/jobs/{job_id}/downloads",
	response_model=BrowserDownloadsResponse,
	dependencies=[Depends(require_auth)],
)
async def list_job_downloads(
	job_id: str,
	session_id: str = Depends(require_session),
) -> BrowserDownloadsResponse:
	return BrowserDownloadsResponse(files=await manager.downloads(session_id, job_id))


@app.get(
	"/v1/jobs/{job_id}/downloads/{name}",
	dependencies=[Depends(require_auth)],
	response_class=Response,
)
async def get_job_download(
	job_id: str,
	name: str,
	session_id: str = Depends(require_session),
) -> Response:
	download, data = await manager.download(session_id, job_id, name)
	return Response(content=data, media_type=download.mime_type)


@app.get(
	"/v1/tabs",
	response_model=BrowserTabsResponse,
	dependencies=[Depends(require_auth)],
)
async def list_tabs(
	session_id: str = Depends(require_session),
) -> BrowserTabsResponse:
	return BrowserTabsResponse(tabs=await manager.tabs(session_id))


@app.post(
	"/v1/tabs/{tab_id}/close",
	response_model=BrowserTabsResponse,
	dependencies=[Depends(require_auth)],
)
async def close_tab(
	tab_id: str,
	session_id: str = Depends(require_session),
) -> BrowserTabsResponse:
	return BrowserTabsResponse(tabs=await manager.close_tab(session_id, tab_id))
