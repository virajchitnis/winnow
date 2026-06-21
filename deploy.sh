#!/usr/bin/env bash
# Deploy Winnow to a remote server over SSH.
#
# Reads target details from .env.deploy (gitignored — your server hostname,
# user, and path are NEVER committed). The image is built remotely inside
# Docker, so the server needs only Docker; it does not need Go.
set -euo pipefail

cd "$(dirname "$0")"

if [[ ! -f .env.deploy ]]; then
  echo "error: .env.deploy not found. Copy .env.deploy.example to .env.deploy and fill it in." >&2
  exit 1
fi
# shellcheck disable=SC1091
source .env.deploy

: "${DEPLOY_HOST:?set DEPLOY_HOST in .env.deploy}"
: "${DEPLOY_PATH:?set DEPLOY_PATH in .env.deploy}"
DEPLOY_TRANSPORT="${DEPLOY_TRANSPORT:-rsync}"

ssh_target="$DEPLOY_HOST"
[[ -n "${DEPLOY_USER:-}" ]] && ssh_target="${DEPLOY_USER}@${DEPLOY_HOST}"

echo ">> ensuring remote path ${DEPLOY_PATH}"
ssh "$ssh_target" "mkdir -p '${DEPLOY_PATH}'"

case "$DEPLOY_TRANSPORT" in
  rsync)
    echo ">> syncing source via rsync (excluding secrets and local state)"
    rsync -az --delete \
      --exclude '.git' \
      --exclude '.env' --exclude '.env.deploy' --exclude 'winnow.env' \
      --exclude '*.db' --exclude 'data/' \
      --exclude 'cloudflared/' \
      ./ "${ssh_target}:${DEPLOY_PATH}/"
    ;;
  git)
    echo ">> pulling latest on the server"
    ssh "$ssh_target" "cd '${DEPLOY_PATH}' && git pull --ff-only"
    ;;
  *)
    echo "error: unknown DEPLOY_TRANSPORT '${DEPLOY_TRANSPORT}' (use rsync or git)" >&2
    exit 1
    ;;
esac

echo ">> building and (re)starting containers remotely"
# The server must have a .env present at ${DEPLOY_PATH}/.env (copied out-of-band
# once, or managed via your secrets tooling) — it is never synced from here.
ssh "$ssh_target" "cd '${DEPLOY_PATH}' && docker compose up -d --build"

echo ">> done. Tail logs with: ssh ${ssh_target} 'cd ${DEPLOY_PATH} && docker compose logs -f winnow'"
