#!/usr/bin/env bash
# Build and (re)start the airband stack on a host over ssh, without a registry.
#
#   cp deploy.env.example deploy.env   # once; set DEPLOY_HOST, DEPLOY_DIR and AIRBAND_SRC
#   scripts/deploy.sh                  # sync, build on the host, restart
#   scripts/deploy.sh --logs           # same, then follow the logs (ctrl-c to stop)
#
# On the host, DEPLOY_DIR ends up with:
#   src/          this repository (without .git and bin/)
#   airband-src/  the RTLSDR-Airband checkout at AIRBAND_SRC, if set (with .git, for the version)
#   compose.yml, compose.legacy.yml   copies of deploy/
#   data/         the hub's waveform cache
#   .env          settings. Created on first deploy with a random HUB_SOURCE_PASSWORD and
#                 never overwritten; readable only by its owner. See README "Deploying".
#
# The rtl-airband image is built whenever AIRBAND_SRC is set, but only runs when .env has
# COMPOSE_PROFILES=airband, so it can be prepared while another container owns the SDR.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=/dev/null
[ -f "${REPO}/deploy.env" ] && . "${REPO}/deploy.env"
: "${DEPLOY_HOST:?set DEPLOY_HOST (see deploy.env.example)}"
: "${DEPLOY_DIR:?set DEPLOY_DIR (see deploy.env.example)}"

echo "==> Syncing to ${DEPLOY_HOST}:${DEPLOY_DIR}"
# shellcheck disable=SC2029  # DEPLOY_DIR is local config, meant to expand here
ssh "${DEPLOY_HOST}" "mkdir -p '${DEPLOY_DIR}/src' '${DEPLOY_DIR}/data' '${DEPLOY_DIR}/recordings'"
rsync -a --delete --exclude '.git/' --exclude 'bin/' --exclude 'deploy.env' --exclude 'web/node_modules/' \
	"${REPO}/" "${DEPLOY_HOST}:${DEPLOY_DIR}/src/"
rsync -a "${REPO}/deploy/compose.yml" "${REPO}/deploy/compose.legacy.yml" "${DEPLOY_HOST}:${DEPLOY_DIR}/"
if [ -n "${AIRBAND_SRC:-}" ]; then
	echo "==> Syncing ${AIRBAND_SRC} (rtl_airband)"
	rsync -a --delete --exclude 'build/' --exclude 'builds/' --exclude '.venv/' \
		--exclude 'system_tests/.generated_input/' --exclude 'system_tests/test_output/' \
		--exclude 'system_tests/test_cases/' \
		"${AIRBAND_SRC%/}/" "${DEPLOY_HOST}:${DEPLOY_DIR}/airband-src/"
fi

echo "==> Building and starting"
# shellcheck disable=SC2029
ssh "${DEPLOY_HOST}" "cd '${DEPLOY_DIR}' && \
	if [ ! -f .env ]; then \
		umask 077 && printf 'HUB_SOURCE_PASSWORD=%s\n' \"\$(head -c 18 /dev/urandom | base64 | tr -d '/+=')\" > .env && \
		echo 'Created .env with a random HUB_SOURCE_PASSWORD'; \
	fi && \
	if [ -d airband-src ]; then docker compose --profile airband build rtl-airband; fi && \
	docker compose up -d --build --remove-orphans && docker compose ps"

if [ "${1:-}" = "--logs" ]; then
	# shellcheck disable=SC2029
	ssh "${DEPLOY_HOST}" "cd '${DEPLOY_DIR}' && docker compose logs -f --tail 20"
fi
