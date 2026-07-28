ARG PYTHON_IMAGE=docker.arvancloud.ir/library/python:3.12-slim
FROM ${PYTHON_IMAGE}

ARG APP_UID=911
ARG APP_GID=911
ARG DEBIAN_MIRROR=https://archive.debian.petiak.ir/debian
ARG DEBIAN_SECURITY_MIRROR=https://mirrors.tuna.tsinghua.edu.cn/debian-security

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    BROWSER_USE_SETUP_LOGGING=true \
    IN_DOCKER=true \
    BROWSER_USE_EXECUTABLE_PATH=/usr/bin/chromium \
    BROWSER_USE_DATA_DIR=/data

RUN sed -i \
        -e "s|http://deb.debian.org/debian-security|${DEBIAN_SECURITY_MIRROR}|g" \
        -e "s|http://deb.debian.org/debian|${DEBIAN_MIRROR}|g" \
        /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && apt-get install --no-install-recommends -y \
        chromium \
        curl \
        fonts-liberation \
        fonts-noto-color-emoji \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid "${APP_GID}" browseruse \
    && useradd --uid "${APP_UID}" --gid "${APP_GID}" --create-home browseruse

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY app ./app

RUN mkdir -p /data \
    && chown -R browseruse:browseruse /app /data

USER browseruse

EXPOSE 8087

HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=5 \
    CMD curl --fail --silent http://127.0.0.1:8087/health >/dev/null || exit 1

CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8087", "--no-access-log"]
