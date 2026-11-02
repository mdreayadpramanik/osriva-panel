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
fail() { echo "FAILED: $*" >&2; exit 1; }

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

step "tasks: list services and processes, control a service (agent runs as root via systemd)"
task() { # task TYPE PARAMS_JSON -> final task JSON
	local t id i s
	t="$(api POST "/servers/$SERVER_ID/tasks" "$(jq -nc --arg t "$1" --argjson p "$2" '{type:$t,params:$p}')")"
	id="$(jq -r .id <<<"$t")"
	for i in 1 2 3; do
		t="$(api GET "/servers/$SERVER_ID/tasks/$id?wait=20")"
		s="$(jq -r .status <<<"$t")"
		[ "$s" = queued ] || [ "$s" = running ] || break
	done
	echo "$t"
}
SERVICES="$(task services.list '{}')"
jq -e '.status == "succeeded" and .result.manager == "systemd" and (.result.services | length > 20)' <<<"$SERVICES" >/dev/null
SVC="$(jq -r '[.result.services[] | select(.state == "running" and (.name == "cron.service" or .name == "rsyslog.service" or .name == "atd.service"))][0].name' <<<"$SERVICES")"
[ -n "$SVC" ] && [ "$SVC" != null ] || { echo "no safe running service to control"; exit 1; }
echo "controlling $SVC"
task processes.list '{}' | jq -e '.status == "succeeded" and (.result.processes | length > 10) and ([.result.processes[] | select(.name == "osriva-agent")] | length == 1)' >/dev/null
task service.action "{\"name\":\"$SVC\",\"action\":\"stop\"}" | jq -e '.status == "succeeded" and .result.state == "stopped"'
! systemctl is-active --quiet "$SVC" || fail 'negated check failed (see line above)'
task service.action "{\"name\":\"$SVC\",\"action\":\"start\"}" | jq -e '.status == "succeeded" and .result.state == "running"'
systemctl is-active --quiet "$SVC"
task service.action "{\"name\":\"$SVC\",\"action\":\"restart\"}" | jq -e '.status == "succeeded"'
# The control plane refuses anything outside the typed allowlist.
! api POST "/servers/$SERVER_ID/tasks" '{"type":"shell.exec","params":{"cmd":"id"}}' >/dev/null 2>&1 || fail 'negated check failed (see line above)'
! api POST "/servers/$SERVER_ID/tasks" '{"type":"service.action","params":{"name":"x;reboot","action":"stop"}}' >/dev/null 2>&1 || fail 'negated check failed (see line above)'
api GET "/servers/$SERVER_ID/tasks" | jq -e '[.tasks[] | select(.type == "service.action")] | length == 3' >/dev/null

step "logs: journald (whole journal and one unit)"
task logs.read '{"source":"system","lines":50}' | jq -e '.status == "succeeded" and .result.backend == "journald" and (.result.entries | length > 0)' >/dev/null
task logs.read "{\"source\":\"service\",\"service\":\"osriva-agent.service\",\"lines\":20}" | jq -e '.status == "succeeded" and ([.result.entries[] | select(.source == "osriva-agent.service")] | length > 0)' >/dev/null

step "files: browse, write, read back, delete; secrets stay out of reach (agent runs as root)"
sudo mkdir -p /srv/osriva-system-test
task files.list '{"path":"/etc"}' | jq -e '.status == "succeeded" and ([.result.entries[] | select(.name == "shadow" and .denied == true)] | length == 1) and (.result.roots | index("/etc") != null)' >/dev/null
task files.read '{"path":"/etc/hostname"}' | jq -e --arg h "$(cat /etc/hostname)" '.status == "succeeded" and (.result.content | rtrimstr("\n")) == $h' >/dev/null
task files.write '{"path":"/srv/osriva-system-test/app.conf","content":"password=hunter2\n","create":true}' | jq -e '.status == "succeeded" and .result.created == true' >/dev/null
[ "$(sudo cat /srv/osriva-system-test/app.conf)" = "password=hunter2" ] || fail "written file content differs"
task files.read '{"path":"/srv/osriva-system-test/app.conf"}' | jq -e '.result.content == "password=hunter2\n"' >/dev/null
for denied in /etc/shadow /etc/sudoers /etc/osriva-agent/agent.key /etc/osriva/osriva.env /root/.bashrc /proc/1/environ /etc/../root/.profile; do
	task files.read "{\"path\":\"$denied\"}" | jq -e '.status == "failed" and (.error | contains("not allowed by this agent"))' >/dev/null || fail "$denied was readable"
done
sudo ln -sfn /root /srv/osriva-system-test/escape
task files.list '{"path":"/srv/osriva-system-test/escape"}' | jq -e '.status == "failed" and (.error | contains("not allowed"))' >/dev/null || fail "symlink escape to /root"
task files.delete '{"path":"/srv/osriva-system-test/app.conf"}' | jq -e '.status == "succeeded"' >/dev/null
[ ! -e /srv/osriva-system-test/app.conf ] || fail "file not deleted"
task files.delete '{"path":"/srv"}' | jq -e '.status == "failed"' >/dev/null || fail "a root was deleted"
api GET "/servers/$SERVER_ID/tasks?limit=50" | grep -q hunter2 && fail "file content leaked into the task list"
sudo rm -rf /srv/osriva-system-test

step "users, SSH keys, cron, hostname and timezone (agent runs as root)"
task users.list '{}' | jq -e '.status == "succeeded" and ([.result.users[] | select(.name == "root")] | length == 1)' >/dev/null
sudo useradd --create-home --shell /bin/bash osriva-ci
KEY_DIR="$(mktemp -d)"
ssh-keygen -q -t ed25519 -N '' -C osriva-ci-key -f "$KEY_DIR/id"
PUB="$(cat "$KEY_DIR/id.pub")"
FP="$(ssh-keygen -lf "$KEY_DIR/id.pub" | awk '{print $2}')"
ADD="$(task sshkeys.add "$(jq -nc --arg k "$PUB" '{user:"osriva-ci",key:$k}')")"
jq -e '.status == "succeeded" and .result.added == true' <<<"$ADD" >/dev/null || fail "sshkeys.add: $ADD"
sudo stat -c '%a %U' /home/osriva-ci/.ssh | grep -qx '700 osriva-ci' || fail "~/.ssh owner/mode"
sudo stat -c '%a %U' /home/osriva-ci/.ssh/authorized_keys | grep -qx '600 osriva-ci' || fail "authorized_keys owner/mode"
task sshkeys.list '{"user":"osriva-ci"}' | jq -e --arg fp "$FP" '[.result.keys[] | select(.fingerprint == $fp)] | length == 1' >/dev/null
# A key with forced-command options is refused before it reaches the agent.
! api POST "/servers/$SERVER_ID/tasks" "$(jq -nc --arg k "command=\"/bin/true\" $PUB" '{type:"sshkeys.add",params:{user:"osriva-ci",key:$k}}')" >/dev/null 2>&1 || fail 'key with options was accepted'
task sshkeys.remove "$(jq -nc --arg fp "$FP" '{user:"osriva-ci",fingerprint:$fp}')" | jq -e '.status == "succeeded" and .result.removed == 1' >/dev/null
sudo grep -q osriva-ci-key /home/osriva-ci/.ssh/authorized_keys && fail "key still present after removal"
# A planted symlink must not make root write elsewhere.
sudo rm -f /home/osriva-ci/.ssh/authorized_keys
sudo ln -s /etc/shadow /home/osriva-ci/.ssh/authorized_keys
task sshkeys.add "$(jq -nc --arg k "$PUB" '{user:"osriva-ci",key:$k}')" | jq -e '.status == "failed" and (.error | contains("symbolic link"))' >/dev/null || fail "followed an authorized_keys symlink"
sudo grep -q osriva-ci-key /etc/shadow && fail "/etc/shadow was modified"
sudo userdel -r osriva-ci 2>/dev/null || true
echo '17 3 * * * root /usr/bin/true osriva-ci-cron' | sudo tee /etc/cron.d/osriva-ci >/dev/null
task cron.list '{}' | jq -e '[.result.entries[] | select(.command | contains("osriva-ci-cron")) | select(.user == "root" and .schedule == "17 3 * * *")] | length == 1' >/dev/null
sudo rm -f /etc/cron.d/osriva-ci
OLD_TZ="$(timedatectl show --property=Timezone --value)"
task system.timezone '{"timezone":"Asia/Dhaka"}' | jq -e '.status == "succeeded" and .result.timezone == "Asia/Dhaka"' >/dev/null
[ "$(timedatectl show --property=Timezone --value)" = Asia/Dhaka ] || fail "timezone not applied"
task system.timezone '{"timezone":"Mars/Olympus_Mons"}' | jq -e '.status == "failed"' >/dev/null || fail "unknown timezone accepted"
task system.timezone "$(jq -nc --arg tz "$OLD_TZ" '{timezone:$tz}')" >/dev/null
OLD_HN="$(hostname)"
task system.hostname '{"hostname":"osriva-ci-host"}' | jq -e '.status == "succeeded" and .result.hostname == "osriva-ci-host"' >/dev/null
[ "$(hostname)" = osriva-ci-host ] || fail "hostname not applied"
task system.hostname "$(jq -nc --arg h "$OLD_HN" '{hostname:$h}')" >/dev/null

step "live metrics arrive (CPU needs two readings: first upload after ~30 s)"
has_metrics() { api GET "/servers/$SERVER_ID/metrics?minutes=10" | jq -e '[.samples[] | select(.cpu_percent != null and .mem_total_bytes > 0 and .load1 != null)] | length > 0'; }
wait_for 90 "metrics with CPU, memory and load" has_metrics

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
! systemctl is-active --quiet osriva-agent || fail 'negated check failed (see line above)'
[ "$(systemctl show -p ExecMainStatus --value osriva-agent)" = 3 ]

step "uninstall"
sudo /usr/local/bin/osriva-agent service uninstall
[ ! -e /etc/systemd/system/osriva-agent.service ]
sudo "$INSTALLER" uninstall --purge --yes
[ ! -e /opt/osriva ] && [ ! -e /etc/osriva ]
[ -z "$(sudo docker ps -aq --filter label=com.docker.compose.project=osriva)" ]
echo; echo "SYSTEM TEST PASSED"
