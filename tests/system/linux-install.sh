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
task() { # task TYPE PARAMS_JSON [WAIT_ROUNDS] -> final task JSON
	local t id i s
	t="$(api POST "/servers/$SERVER_ID/tasks" "$(jq -nc --arg t "$1" --argjson p "$2" '{type:$t,params:$p}')")"
	id="$(jq -r .id <<<"$t")"
	for i in $(seq "${3:-3}"); do
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


step "Docker: the control plane itself runs in Docker on this host (agent as root)"
login # fresh sign-in: run/stop/remove need recent authentication
task docker.info '{}' | jq -e '.status == "succeeded" and .result.available == true and .result.containers_running >= 4' >/dev/null || fail "docker.info"
CTS="$(task docker.containers '{}')"
jq -e '[.result[] | select(.compose_project == "osriva" and .protected == true)] | length >= 4' <<<"$CTS" >/dev/null || fail "control-plane containers not marked protected: $CTS"
API_CT="$(jq -r '[.result[] | select(.compose_project == "osriva" and (.name | test("api")))][0].name' <<<"$CTS")"
for a in stop restart remove; do
	task docker.container.action "$(jq -nc --arg id "$API_CT" --arg a "$a" '{id:$id,action:$a}')" | jq -e '.status == "failed" and (.error | contains("control plane"))' >/dev/null || fail "$a on $API_CT was not refused"
done
[ "$(sudo docker inspect -f '{{.State.Running}}' "$API_CT")" = true ] || fail "$API_CT is not running any more"
task docker.container.run '{"image":"alpine:3.20","volumes":[{"volume":"osriva_postgres-data","target":"/steal"}]}' | jq -e '.status == "failed" and (.error | contains("control plane"))' >/dev/null || fail "control-plane volume was mountable"
for bad in '{"image":"alpine:3.20","privileged":true}' '{"image":"alpine:3.20","network_mode":"host"}' '{"image":"alpine:3.20","volumes":[{"volume":"/","target":"/host"}]}'; do
	api POST "/servers/$SERVER_ID/tasks" "$(jq -nc --argjson p "$bad" '{type:"docker.container.run",params:$p}')" >/dev/null 2>&1 && fail "unsafe run accepted: $bad"
done

step "Docker: run, inspect, log, stop and remove a container"
RUN="$(task docker.container.run '{"image":"nginx:1.27-alpine","name":"osriva-ci-web","env":{"CI_SECRET":"hunter3"},"ports":[{"host_port":18080,"container_port":80}],"volumes":[{"volume":"osriva-ci-data","target":"/usr/share/nginx/html"}],"restart":"unless-stopped"}' 9)"
jq -e '.status == "succeeded" and .result.state == "running" and .result.name == "osriva-ci-web"' <<<"$RUN" >/dev/null || fail "docker run: $RUN"
[ "$(sudo docker inspect -f '{{(index (index .HostConfig.PortBindings "80/tcp") 0).HostIp}}' osriva-ci-web)" = 127.0.0.1 ] || fail "port not bound to loopback"
[ "$(sudo docker inspect -f '{{.HostConfig.Privileged}} {{.HostConfig.NetworkMode}}' osriva-ci-web)" = "false bridge" ] || fail "unsafe host config"
sudo docker inspect -f '{{.HostConfig.SecurityOpt}}' osriva-ci-web | grep -q no-new-privileges || fail "no-new-privileges missing"
wait_for 30 "nginx answers on 127.0.0.1:18080" curl -fsS -o /dev/null http://127.0.0.1:18080/ || fail "nginx not reachable on loopback"
task docker.container.logs '{"id":"osriva-ci-web","lines":50}' | jq -e '.status == "succeeded" and (.result.lines | length > 0)' >/dev/null || fail "container logs"
api GET "/servers/$SERVER_ID/tasks?limit=50" | grep -q hunter3 && fail "container env value leaked into the task list"
task docker.container.action '{"id":"osriva-ci-web","action":"stop"}' | jq -e '.status == "succeeded" and .result.state == "exited"' >/dev/null || fail "docker stop"
task docker.container.action '{"id":"osriva-ci-web","action":"start"}' | jq -e '.status == "succeeded" and .result.state == "running"' >/dev/null || fail "docker start"
task docker.container.action '{"id":"osriva-ci-web","action":"remove"}' | jq -e '.status == "failed"' >/dev/null || fail "a running container was removed without stopping"
task docker.container.action '{"id":"osriva-ci-web","action":"stop"}' >/dev/null
task docker.container.action '{"id":"osriva-ci-web","action":"remove"}' | jq -e '.status == "succeeded"' >/dev/null || fail "docker remove"
sudo docker inspect osriva-ci-web >/dev/null 2>&1 && fail "container still exists"
task docker.volumes '{}' | jq -e '[.result[] | select(.name == "osriva-ci-data")] | length == 1' >/dev/null || fail "named volume missing after remove"
task docker.networks '{}' | jq -e '[.result[] | select(.name == "bridge")] | length == 1' >/dev/null || fail "networks"

step "Docker: images"
task docker.image.pull '{"image":"alpine:3.20"}' 9 | jq -e '.status == "succeeded"' >/dev/null || fail "pull"
task docker.images '{}' | jq -e '[.result[] | select(.tags | index("alpine:3.20"))] | length == 1' >/dev/null || fail "pulled image not listed"
task docker.image.remove '{"image":"alpine:3.20"}' | jq -e '.status == "succeeded"' >/dev/null || fail "image remove"
task docker.image.remove '{"image":"postgres:17-alpine"}' | jq -e '.status == "failed"' >/dev/null || fail "an image in use was removed"
task docker.images.prune '{}' | jq -e '.status == "succeeded"' >/dev/null || fail "prune"
sudo docker volume rm osriva-ci-data >/dev/null

step "Deployments: Git (served over HTTP on loopback) → Docker build → swap, recover, rollback, remove"
login # deployments need recent authentication
GITDIR="$(mktemp -d)"
gitc() { git -C "$GITDIR/work" -c user.name=ci -c user.email=ci@example.com "$@"; }
publish() { # commit the work tree and refresh the bare repository served over dumb HTTP
	gitc add -A && gitc commit -qm "$1" && gitc push -q origin main
	git -C "$GITDIR/site.git" update-server-info
}
git init -q -b main "$GITDIR/work"
mkdir -p "$GITDIR/work/site" "$GITDIR/work/api"
echo '<h1>release v1</h1>' >"$GITDIR/work/site/index.html"
cat >"$GITDIR/work/api/package.json" <<'JSON'
{"name":"ci-api","version":"1.0.0","scripts":{"start":"node server.js"}}
JSON
echo 'require("http").createServer((q, r) => r.end("node ok " + process.env.PORT)).listen(process.env.PORT);' >"$GITDIR/work/api/server.js"
gitc add -A && gitc commit -qm v1
git clone -q --bare "$GITDIR/work" "$GITDIR/site.git"
gitc remote add origin "$GITDIR/site.git"
git -C "$GITDIR/site.git" update-server-info
python3 -m http.server 18090 --bind 127.0.0.1 --directory "$GITDIR" >/dev/null 2>&1 &
HTTP_PID=$!
wait_for 10 "git HTTP server" curl -fsS -o /dev/null http://127.0.0.1:18090/site.git/info/refs || fail "git HTTP server"
REPO_URL="http://127.0.0.1:18090/site.git"

deploy_wait() { # deploy_wait APP_ID DEPLOYMENT_ID -> final deployment JSON
	local d s i
	for i in $(seq 1 120); do
		d="$(api GET "/servers/$SERVER_ID/apps/$1/deployments/$2")"
		s="$(jq -r .status <<<"$d")"
		[ "$s" = queued ] || [ "$s" = running ] || break
		sleep 5
	done
	echo "$d"
}
deploy() { # deploy APP_ID -> final deployment JSON
	local id
	id="$(api POST "/servers/$SERVER_ID/apps/$1/deployments" | jq -r .id)"
	deploy_wait "$1" "$id"
}

SITE="$(api POST "/servers/$SERVER_ID/apps" "$(jq -nc --arg u "$REPO_URL" '{name:"site",git_url:$u,git_ref:"main",build_mode:"static",build_context:"site",container_port:80,host_port:18081,env:{CI_SECRET:"hunter5"}}')" | jq -r .id)"
[ -n "$SITE" ] && [ "$SITE" != null ] || fail "create app"
D1="$(deploy "$SITE")"
jq -e --arg c "$(gitc rev-parse HEAD)" '.status == "succeeded" and .result.commit == $c and .result.state == "running" and (.result.logs | contains("building"))' <<<"$D1" >/dev/null || fail "first deployment: $D1"
D1_ID="$(jq -r .id <<<"$D1")"
curl -fsS http://127.0.0.1:18081/ | grep -q "release v1" || fail "v1 not served"
[ "$(sudo docker inspect -f '{{.HostConfig.Privileged}} {{(index (index .HostConfig.PortBindings "80/tcp") 0).HostIp}} {{index .Config.Labels "dev.osriva.app"}}' osriva-app-site)" = "false 127.0.0.1 site" ] || fail "app container settings"

echo '<h1>release v2</h1>' >"$GITDIR/work/site/index.html"
publish v2
deploy "$SITE" | jq -e '.status == "succeeded"' >/dev/null || fail "second deployment"
curl -fsS http://127.0.0.1:18081/ | grep -q "release v2" || fail "v2 not served"
[ "$(sudo docker ps -aq --filter label=dev.osriva.app=site | wc -l)" = 1 ] || fail "old container left behind"

RB="$(api POST "/servers/$SERVER_ID/apps/$SITE/deployments/$D1_ID/rollback" | jq -r .id)"
deploy_wait "$SITE" "$RB" | jq -e '.status == "succeeded" and .kind == "rollback"' >/dev/null || fail "rollback"
curl -fsS http://127.0.0.1:18081/ | grep -q "release v1" || fail "rollback did not bring v1 back"

step "Deployments: Node.js runtime; a crashing release and a broken build keep the running version"
API_APP="$(api POST "/servers/$SERVER_ID/apps" "$(jq -nc --arg u "$REPO_URL" '{name:"api",git_url:$u,git_ref:"main",build_mode:"node",build_context:"api",container_port:3000,host_port:18082}')" | jq -r .id)"
deploy "$API_APP" | jq -e '.status == "succeeded"' >/dev/null || fail "node deployment"
curl -fsS http://127.0.0.1:18082/ | grep -qx "node ok 3000" || fail "node app not served (PORT default)"
echo 'process.exit(1);' >"$GITDIR/work/api/server.js"
publish crash
CRASH="$(deploy "$API_APP")"
jq -e '.status == "failed" and .result.recovered == true and (.result.logs | contains("previous version"))' <<<"$CRASH" >/dev/null || fail "crashing release: $CRASH"
curl -fsS http://127.0.0.1:18082/ | grep -q "node ok" || fail "previous version not restored"
cat >"$GITDIR/work/api/package.json" <<'JSON'
{"name":"ci-api","version":"1.0.0","scripts":{"start":"node server.js"},"dependencies":{"osriva-ci-package-that-does-not-exist":"9.9.9"}}
JSON
publish broken-build
BROKEN="$(deploy "$API_APP")"
jq -e '.status == "failed" and (.error | contains("build failed")) and (.result.recovered | not)' <<<"$BROKEN" >/dev/null || fail "broken build: $BROKEN"
curl -fsS http://127.0.0.1:18082/ | grep -q "node ok" || fail "running version touched by a failed build"

step "Deployments: secrets are encrypted and not kept in task records; delete removes everything"
pg() { sudo docker compose --project-name osriva --env-file /etc/osriva/osriva.env -f /opt/osriva/current/docker-compose.yml exec -T postgres psql -U osriva -d osriva -tAc "$1"; }
[ "$(pg "SELECT count(*) FROM agent_tasks WHERE params::text LIKE '%hunter5%'")" = 0 ] || fail "secret kept in agent_tasks"
[ "$(pg "SELECT count(*) FROM apps WHERE env_enc IS NOT NULL AND position('hunter5'::bytea in env_enc) > 0")" = 0 ] || fail "secret stored in clear"
[ "$(pg "SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%hunter5%'")" = 0 ] || fail "secret in the audit log"
for app in "$SITE" "$API_APP"; do
	RM="$(api DELETE "/servers/$SERVER_ID/apps/$app" | jq -r .id)"
	t="$(api GET "/servers/$SERVER_ID/tasks/$RM?wait=20")"
	jq -e '.status == "succeeded" and .result.container_removed == true and .result.images_removed > 0' <<<"$t" >/dev/null || fail "app.remove: $t"
done
[ -z "$(sudo docker ps -aq --filter label=dev.osriva.app)" ] || fail "app containers left"
[ -z "$(sudo docker images -q --filter label=dev.osriva.app)" ] || fail "app images left"
kill "$HTTP_PID"

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
