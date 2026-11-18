#!/usr/bin/env bash
# Build and (re)start airband-hub on a host over ssh, without a registry.
#
#   cp deploy.env.example deploy.env   # once; set DEPLOY_HOST and DEPLOY_DIR
#   scripts/deploy.sh                  # sync, build on the host, restart
#   scripts/deploy.sh --logs           # same, then follow the logs (ctrl-c to stop)
#
# On the host, DEPLOY_DIR ends up with:
#   src/         this repository (without .git and bin/)
#   compose.yml  copy of deploy/compose.yml
#   .env         HUB_* settings. Created on first deploy with a random
#                HUB_SOURCE_PASSWORD, never overwritten, readable only by its owner.
#                Optional in it: HUB_HOST_PORT (default 8090) and AIRBAND_NETWORK
#                (default rtlsdr-airband_default).
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=/dev/null
[ -f "${REPO}/deploy.env" ] && . "${REPO}/deploy.env"
: "${DEPLOY_HOST:?set DEPLOY_HOST (see deploy.env.example)}"
: "${DEPLOY_DIR:?set DEPLOY_DIR (see deploy.env.example)}"

echo "==> Syncing to ${DEPLOY_HOST}:${DEPLOY_DIR}"
# shellcheck disable=SC2029  # DEPLOY_DIR is local config, meant to expand here
ssh "${DEPLOY_HOST}" "mkdir -p '${DEPLOY_DIR}/src'"
rsync -a --delete --exclude '.git/' --exclude 'bin/' --exclude 'deploy.env' \
	"${REPO}/" "${DEPLOY_HOST}:${DEPLOY_DIR}/src/"
rsync -a "${REPO}/deploy/compose.yml" "${DEPLOY_HOST}:${DEPLOY_DIR}/compose.yml"

echo "==> Building and starting"
# shellcheck disable=SC2029
ssh "${DEPLOY_HOST}" "cd '${DEPLOY_DIR}' && \
	if [ ! -f .env ]; then \
		umask 077 && printf 'HUB_SOURCE_PASSWORD=%s\n' \"\$(head -c 18 /dev/urandom | base64 | tr -d '/+=')\" > .env && \
		echo 'Created .env with a random HUB_SOURCE_PASSWORD'; \
	fi && \
	docker compose up -d --build && docker compose ps"

if [ "${1:-}" = "--logs" ]; then
	# shellcheck disable=SC2029
	ssh "${DEPLOY_HOST}" "cd '${DEPLOY_DIR}' && docker compose logs -f --tail 20"
fi
