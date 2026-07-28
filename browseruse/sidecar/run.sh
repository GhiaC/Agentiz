#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
EXAMPLE_ENV_FILE="${SCRIPT_DIR}/.env.example"
ACTION="up"
ENV_FILE="${BROWSER_USE_ENV_FILE:-${SCRIPT_DIR}/.env}"

usage() {
	cat <<'EOF'
Usage: ./run.sh [action] [--env-file PATH]

Actions:
  up       Validate config, build, start, and wait for health (default)
  restart  Rebuild and recreate the service, then wait for health
  stop     Stop the service without deleting it
  down     Stop and remove the Compose service/network
  build    Validate config and build the image
  status   Show container status
  logs     Follow service logs
  config   Validate the env file and Compose configuration
  init     Create a secure .env from .env.example without overwriting

Environment:
  BROWSER_USE_ENV_FILE may be used instead of --env-file.
EOF
}

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

info() {
	printf '==> %s\n' "$*"
}

while (($# > 0)); do
	case "$1" in
		--env-file)
			(($# >= 2)) || die "--env-file requires a path"
			ENV_FILE="$2"
			shift 2
			;;
		-h | --help)
			usage
			exit 0
			;;
		up | restart | stop | down | build | status | logs | config | init)
			ACTION="$1"
			shift
			;;
		*)
			die "unknown argument: $1 (use --help)"
			;;
	esac
done

if [[ "${ENV_FILE}" != /* ]]; then
	ENV_FILE="${SCRIPT_DIR}/${ENV_FILE}"
fi

ENV_PARENT="$(dirname -- "${ENV_FILE}")"
[[ -d "${ENV_PARENT}" ]] || die "config directory does not exist: ${ENV_PARENT}"
ENV_PARENT="$(cd -- "${ENV_PARENT}" && pwd -P)"
ENV_FILE="${ENV_PARENT}/$(basename -- "${ENV_FILE}")"

generate_token() {
	if command -v openssl >/dev/null 2>&1; then
		openssl rand -hex 32
		return
	fi
	if [[ -r /dev/urandom ]] && command -v od >/dev/null 2>&1; then
		od -An -N32 -tx1 /dev/urandom | tr -d ' \n'
		return
	fi
	die "cannot generate a token; install openssl"
}

initialize_config() {
	[[ ! -e "${ENV_FILE}" ]] || die "refusing to overwrite existing config: ${ENV_FILE}"
	[[ -f "${EXAMPLE_ENV_FILE}" ]] || die "missing template: ${EXAMPLE_ENV_FILE}"

	local token temporary
	token="$(generate_token)"
	temporary="$(mktemp "${ENV_FILE}.tmp.XXXXXX")"
	if ! awk -v token="${token}" '
		/^BROWSER_USE_SIDECAR_TOKEN=/ {
			print "BROWSER_USE_SIDECAR_TOKEN=" token
			next
		}
		{ print }
	' "${EXAMPLE_ENV_FILE}" >"${temporary}"; then
		rm -f "${temporary}"
		die "failed to render ${ENV_FILE}"
	fi
	chmod 600 "${temporary}"
	mv "${temporary}" "${ENV_FILE}"

	info "created ${ENV_FILE}"
	printf 'Set the API key for BROWSER_USE_LLM_PROVIDER, then run:\n  %s up --env-file %s\n' \
		"${SCRIPT_DIR}/run.sh" "${ENV_FILE}"
}

read_config_value() {
	local key="$1"
	awk -v wanted="${key}" '
		function trim(value) {
			sub(/^[[:space:]]+/, "", value)
			sub(/[[:space:]]+$/, "", value)
			return value
		}
		/^[[:space:]]*(#|$)/ { next }
		{
			line = $0
			sub(/^[[:space:]]*export[[:space:]]+/, "", line)
			separator = index(line, "=")
			if (separator == 0) {
				next
			}
			name = trim(substr(line, 1, separator - 1))
			if (name == wanted) {
				result = trim(substr(line, separator + 1))
			}
		}
		END {
			if ((substr(result, 1, 1) == "\"" && substr(result, length(result), 1) == "\"") ||
				(substr(result, 1, 1) == "\047" && substr(result, length(result), 1) == "\047")) {
				result = substr(result, 2, length(result) - 2)
			}
			print result
		}
	' "${ENV_FILE}"
}

config_value_or_default() {
	local value
	value="$(read_config_value "$1")"
	printf '%s' "${value:-$2}"
}

validate_integer() {
	local name="$1"
	local default="$2"
	local minimum="$3"
	local maximum="$4"
	local value numeric
	value="$(config_value_or_default "${name}" "${default}")"
	[[ "${value}" =~ ^[0-9]+$ ]] || die "${name} must be an integer"
	numeric=$((10#${value}))
	((numeric >= minimum && numeric <= maximum)) ||
		die "${name} must be between ${minimum} and ${maximum}"
}

validate_boolean() {
	local name="$1"
	local default="$2"
	local value
	value="$(config_value_or_default "${name}" "${default}")"
	value="$(printf '%s' "${value}" | tr '[:upper:]' '[:lower:]')"
	case "${value}" in
		1 | 0 | true | false | yes | no | on | off) ;;
		*) die "${name} must be a boolean" ;;
	esac
}

validate_config() {
	[[ -f "${ENV_FILE}" ]] || die "config not found: ${ENV_FILE} (run ./run.sh init)"

	local token provider model provider_key provider_key_name
	token="$(read_config_value BROWSER_USE_SIDECAR_TOKEN)"
	[[ ${#token} -ge 32 && "${token}" != replace-* ]] ||
		die "BROWSER_USE_SIDECAR_TOKEN must be a non-placeholder token of at least 32 characters"

	provider="$(config_value_or_default BROWSER_USE_LLM_PROVIDER openai)"
	provider="$(printf '%s' "${provider}" | tr '[:upper:]' '[:lower:]')"
	model="$(config_value_or_default BROWSER_USE_LLM_MODEL gpt-5-mini)"
	[[ -n "${model}" ]] || die "BROWSER_USE_LLM_MODEL cannot be blank"

	case "${provider}" in
		openai) provider_key_name="OPENAI_API_KEY" ;;
		browser-use) provider_key_name="BROWSER_USE_API_KEY" ;;
		openrouter) provider_key_name="OPENROUTER_API_KEY" ;;
		anthropic) provider_key_name="ANTHROPIC_API_KEY" ;;
		google) provider_key_name="GOOGLE_API_KEY" ;;
		*) die "unsupported BROWSER_USE_LLM_PROVIDER: ${provider}" ;;
	esac
	provider_key="$(read_config_value "${provider_key_name}")"
	[[ -n "${provider_key}" && "${provider_key}" != replace-* ]] ||
		die "${provider_key_name} is required for provider ${provider}"

	validate_integer BROWSER_USE_PORT 8087 1 65535
	validate_integer BROWSER_USE_STARTUP_TIMEOUT_SECONDS 240 30 1800
	validate_integer BROWSER_USE_MAX_CONCURRENT_JOBS 2 1 16
	validate_integer BROWSER_USE_MAX_STEPS 50 1 500
	validate_integer BROWSER_USE_JOB_TIMEOUT_SECONDS 600 30 7200
	validate_integer BROWSER_USE_JOB_TTL_SECONDS 3600 60 86400
	validate_integer BROWSER_USE_MAX_JOBS 1000 10 10000
	validate_boolean BROWSER_USE_AUTO_START_DOCKER true
	validate_boolean BROWSER_USE_HEADLESS true
	validate_boolean BROWSER_USE_DEFAULT_USE_VISION true
	validate_boolean BROWSER_USE_CHROMIUM_SANDBOX false
	validate_boolean BROWSER_USE_BLOCK_IP_ADDRESSES true
}

require_compose() {
	command -v docker >/dev/null 2>&1 || die "docker is not installed"
	docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is required"
	local up_help
	up_help="$(docker compose up --help 2>&1 || true)"
	[[ "${up_help}" == *"--wait"* ]] ||
		die "this Docker Compose version does not support 'up --wait'; upgrade Compose"
}

ensure_docker_engine() {
	if docker info >/dev/null 2>&1; then
		return
	fi

	local auto_start
	auto_start="$(config_value_or_default BROWSER_USE_AUTO_START_DOCKER true)"
	auto_start="$(printf '%s' "${auto_start}" | tr '[:upper:]' '[:lower:]')"
	case "${auto_start}" in
		1 | true | yes | on)
			if command -v colima >/dev/null 2>&1; then
				info "Docker engine is unavailable; starting Colima"
				colima start
			fi
			;;
	esac
	docker info >/dev/null 2>&1 ||
		die "Docker engine is unavailable; start Docker Desktop/Colima and retry"
}

export BROWSER_USE_ENV_FILE="${ENV_FILE}"
COMPOSE=(
	docker compose
	--project-directory "${SCRIPT_DIR}"
	--file "${SCRIPT_DIR}/docker-compose.yml"
	--env-file "${ENV_FILE}"
)

compose_config() {
	"${COMPOSE[@]}" config --quiet
}

show_failure() {
	printf '\nService did not become healthy. Current status:\n' >&2
	"${COMPOSE[@]}" ps >&2 || true
	printf '\nRecent logs:\n' >&2
	"${COMPOSE[@]}" logs --tail 120 browser-use >&2 || true
}

start_service() {
	local recreate_flag="${1:-}"
	local timeout bind_address port health_host
	timeout="$(config_value_or_default BROWSER_USE_STARTUP_TIMEOUT_SECONDS 240)"

	local arguments=(up --build --detach --wait --wait-timeout "${timeout}")
	if [[ -n "${recreate_flag}" ]]; then
		arguments+=("${recreate_flag}")
	fi

	info "building and starting browser-use with ${ENV_FILE}"
	if ! "${COMPOSE[@]}" "${arguments[@]}"; then
		show_failure
		return 1
	fi

	bind_address="$(config_value_or_default BROWSER_USE_BIND_ADDRESS 127.0.0.1)"
	port="$(config_value_or_default BROWSER_USE_PORT 8087)"
	health_host="${bind_address}"
	case "${health_host}" in
		0.0.0.0 | "::" | "[::]") health_host="127.0.0.1" ;;
	esac

	if command -v curl >/dev/null 2>&1; then
		curl --fail --silent --show-error "http://${health_host}:${port}/health" >/dev/null ||
			die "container is healthy but host health endpoint is unreachable"
	fi

	info "browser-use is healthy at http://${health_host}:${port}"
	info "Go client token: BROWSER_USE_SIDECAR_TOKEN from ${ENV_FILE}"
}

if [[ "${ACTION}" == "init" ]]; then
	initialize_config
	exit 0
fi

if [[ ! -f "${ENV_FILE}" ]]; then
	if [[ "${ACTION}" == "up" ]]; then
		initialize_config
		exit 2
	fi
	die "config not found: ${ENV_FILE} (run ./run.sh init)"
fi

require_compose

case "${ACTION}" in
	config)
		validate_config
		compose_config
		info "configuration is valid: ${ENV_FILE}"
		;;
	up)
		validate_config
		compose_config
		ensure_docker_engine
		start_service
		;;
	restart)
		validate_config
		compose_config
		ensure_docker_engine
		start_service --force-recreate
		;;
	build)
		validate_config
		compose_config
		ensure_docker_engine
		"${COMPOSE[@]}" build
		;;
	stop)
		compose_config
		ensure_docker_engine
		"${COMPOSE[@]}" stop
		;;
	down)
		compose_config
		ensure_docker_engine
		"${COMPOSE[@]}" down
		;;
	status)
		compose_config
		ensure_docker_engine
		"${COMPOSE[@]}" ps
		;;
	logs)
		compose_config
		ensure_docker_engine
		"${COMPOSE[@]}" logs --follow --tail 200 browser-use
		;;
esac
