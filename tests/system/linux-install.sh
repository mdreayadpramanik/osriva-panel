#!/usr/bin/env bash
# System test on a disposable Linux host (the GitHub Actions Ubuntu runner).
# Installs the full control plane with the real installer, connects a real
# agent as a systemd service, then exercises backup, upgrade, rollback, revoke
# and uninstall. NEVER run this on a machine you care about: it installs and
# finally purges /opt/osriva, /etc/osriva and the osriva-agent service.
set -Eeuo pipefail
trap 'echo "FAILED at line $LINENO: $BASH_COMMAND" >&2' ERR

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
INSTALLER="$ROOT/installer/osriva-install.sh"
PANEL="https://localhost"
JAR="$(mktemp)"
EMAIL="ci-owner@example.com"
step() { echo; echo "######## $*"; }

[ "${CI:-}" = true ] || { echo "refusing to run outside CI (set CI=true if this machine is disposable)"; exit 1; }

api() { # api METHOD PATH [JSON]
	local args=(-fsS --max-time 20 -b "$JAR" -c "$JAR" -X "$1" "$PANEL/api/v1$2" -H "X-CSRF-Token: ${CSRF:-}")
	[ $# -ge 3 ] && args+=(-H "Content-Type: application/json" -d "$3")
	curl "${args[@]}"
}
login() {
	CSRF="$(api POST /auth/login "$(jq -nc --arg e "$EMAIL" --arg p "$PASSWORD" '{email:$e,password:$p}')" | jq -r .csrf_token)"
	[ -n "$CSRF" ] && [ "$CSRF" != null ]
}
wait_for() { # wait_for SECONDS DESCRIPTION COMMAND...
	local n="$1" what="$2" i
	shift 2
	for i in $(seq 1 "$n"); do
		if "$@" >/dev/null 2>&1; then echo "ok: $what"; return 0; fi
		sleep 1
	done
	echo "timed out waiting for: $what" >&2
	return 1
}
server_connection() { api GET "/servers/$SERVER_ID" | jq -r .server.connection; }
is_online() { [ "$(server_connection)" = online ]; }

step "plan (must change nothing)"
sudo "$INSTALLER" plan --domain localhost --agent
[ ! -e /opt/osriva ] || { echo "plan created /opt/osriva"; exit 1; }

step "install"
sudo "$INSTALLER" install --yes --domain localhost --agent --admin-email "$EMAIL" | tee /tmp/install.log
PASSWORD="$(sed -n 's/^generated password: //p' /tmp/install.log)"
[ -n "$PASSWORD" ] || { echo "no generated password in installer output"; exit 1; }
echo "::add-mask::$PASSWORD"
sudo stat -c '%a %U' /etc/osriva/osriva.env | grep -qx '600 root'

step "trust Caddy's local CA (localhost certificate) on this runner"
CADDY="$(sudo docker compose --project-name osriva --env-file /etc/osriva/osriva.env -f /opt/osriva/current/docker-compose.yml ps -q caddy)"
sudo docker cp "$CADDY:/data/caddy/pki/authorities/local/root.crt" /usr/local/share/ca-certificates/osriva-caddy-local.crt
sudo update-ca-certificates >/dev/null
curl -fsS "$PANEL/api/v1/meta" | jq -e '.name == "OSRIVA Panel"'

step "owner login, add a server"
login
api GET /servers/agent-endpoint | jq -e '.configured == true'
api GET /agent/downloads | jq -e '[.builds[] | .os + "/" + .arch] | index("linux/amd64") != null'
CREATED="$(api POST /servers '{"name":"ci-runner"}')"
SERVER_ID="$(jq -r .server.id <<<"$CREATED")"
INSTALL_CMD="$(jq -r .enrollment.install_unix <<<"$CREATED")"
[[ "$INSTALL_CMD" == curl* ]] || { echo "no unix install command: $CREATED"; exit 1; }

step "one-line agent install (download, checksum, enrol, systemd service)"
bash -c "$INSTALL_CMD"
systemctl is-active --quiet osriva-agent
sudo stat -c '%a' /etc/osriva-agent | grep -qx 700
wait_for 90 "server online" is_online
api GET "/servers/$SERVER_ID" | jq -e '.inventory.os.family == "linux" and (.capabilities[] | select(.key == "agent.inventory") | .status == "SUPPORTED")'

step "backup"
sudo "$INSTALLER" backup
sudo test -s /var/backups/osriva/latest/database.sql.gz

step "upgrade (same source as a new release) keeps data"
sudo "$INSTALLER" upgrade --yes
login
wait_for 150 "server online after upgrade" is_online

step "rollback restores the previous release and the backup"
sudo "$INSTALLER" rollback --yes
sudo "$INSTALLER" verify
login
wait_for 150 "server online after rollback" is_online

step "repair"
sudo "$INSTALLER" repair --yes
login

step "revoking the server stops the agent for good (exit code 3, no restart loop)"
api POST "/servers/$SERVER_ID/revoke" >/dev/null
[ "$(server_connection)" = revoked ]
wait_for 150 "agent service stopped" sh -c '! systemctl is-active --quiet osriva-agent'
sleep 15 # longer than RestartSec: it must not come back
! systemctl is-active --quiet osriva-agent
[ "$(systemctl show -p ExecMainStatus --value osriva-agent)" = 3 ]

step "uninstall"
sudo /usr/local/bin/osriva-agent service uninstall
[ ! -e /etc/systemd/system/osriva-agent.service ]
sudo "$INSTALLER" uninstall --purge --yes
[ ! -e /opt/osriva ] && [ ! -e /etc/osriva ]
[ -z "$(sudo docker ps -aq --filter label=com.docker.compose.project=osriva)" ]
echo; echo "SYSTEM TEST PASSED"
