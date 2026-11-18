#!/usr/bin/env bash
# Deploy the airband stack to a host over ssh.
#
#   cp deploy.env.example deploy.env   # once; set DEPLOY_HOST, DEPLOY_DIR (and AIRBAND_SRC for --build)
#   scripts/deploy.sh                  # copy the compose files, pull the published images, restart
#   scripts/deploy.sh --build          # sync both sources and build the images on the host instead
#   scripts/deploy.sh --logs           # either of the above, then follow the logs (ctrl-c to stop)
#
# On the host, DEPLOY_DIR ends up with:
#   compose.yml, compose.legacy.yml, compose.build.yml   copies of deploy/
#   src/, airband-src/   with --build: this repository and the RTLSDR-Airband checkout at
#                        AIRBAND_SRC (with .git, for the version)
#   data/         the hub's waveform cache
#   .env          settings. Created on first deploy with a random HUB_SOURCE_PASSWORD and
#                 never overwritten; readable only by its owner. See README "Stack settings".
#
# rtl-airband only runs when .env has COMPOSE_PROFILES=airband, so the stack can be prepared
# while another container still owns the SDR.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=/dev/null
[ -f "${REPO}/deploy.env" ] && . "${REPO}/deploy.env"
: "${DEPLOY_HOST:?set DEPLOY_HOST (see deploy.env.example)}"
: "${DEPLOY_DIR:?set DEPLOY_DIR (see deploy.env.example)}"

BUILD=false
LOGS=false
for arg in "$@"; do
	case "$arg" in
	--build) BUILD=true ;;
	--logs) LOGS=true ;;
	*)
		echo "unknown option: $arg" >&2
		exit 2
		;;
	esac
done

# shellcheck disable=SC2029  # DEPLOY_DIR is local config, meant to expand here
ssh "${DEPLOY_HOST}" "mkdir -p '${DEPLOY_DIR}/data' '${DEPLOY_DIR}/recordings'"
rsync -a "${REPO}/deploy/compose.yml" "${REPO}/deploy/compose.legacy.yml" "${REPO}/deploy/compose.build.yml" \
	"${DEPLOY_HOST}:${DEPLOY_DIR}/"

if $BUILD; then
	echo "==> Syncing this repository to ${DEPLOY_HOST}:${DEPLOY_DIR}/src"
	rsync -a --delete --exclude '.git/' --exclude 'bin/' --exclude 'deploy.env' --exclude 'web/node_modules/' \
		"${REPO}/" "${DEPLOY_HOST}:${DEPLOY_DIR}/src/"
	if [ -n "${AIRBAND_SRC:-}" ]; then
		echo "==> Syncing ${AIRBAND_SRC} to ${DEPLOY_HOST}:${DEPLOY_DIR}/airband-src"
		rsync -a --delete --exclude 'build/' --exclude 'builds/' --exclude '.venv/' \
			--exclude 'system_tests/.generated_input/' --exclude 'system_tests/test_output/' \
			--exclude 'system_tests/test_cases/' \
			"${AIRBAND_SRC%/}/" "${DEPLOY_HOST}:${DEPLOY_DIR}/airband-src/"
	fi
fi

echo "==> $($BUILD && echo Building || echo Pulling) and starting"
ssh "${DEPLOY_HOST}" bash -s -- "${DEPLOY_DIR}" "$($BUILD && echo build || echo pull)" <<'REMOTE'
set -euo pipefail
cd "$1"
if [ ! -f .env ]; then
	(umask 077 && printf 'HUB_SOURCE_PASSWORD=%s\n' "$(head -c 18 /dev/urandom | base64 | tr -d '/+=')" > .env)
	echo "Created .env with a random HUB_SOURCE_PASSWORD"
fi
if [ "$2" = build ]; then
	# the build override goes after whatever .env selects (e.g. compose.legacy.yml)
	files=$(sed -n 's/^COMPOSE_FILE=//p' .env | head -n 1)
	export COMPOSE_FILE="${files:-compose.yml}:compose.build.yml"
	if [ -d airband-src ]; then docker compose --profile airband build rtl-airband; fi
	docker compose build airband-hub
else
	# also pulls rtl-airband while its profile is off, so it is ready for the switch
	docker compose --profile airband pull
fi
docker compose up -d --remove-orphans
docker compose ps
REMOTE

if $LOGS; then
	# shellcheck disable=SC2029
	ssh "${DEPLOY_HOST}" "cd '${DEPLOY_DIR}' && docker compose logs -f --tail 20"
fi
