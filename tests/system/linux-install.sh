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
	for i in $(seq "$(( ${3:-3} * 4 / 3 + 1 ))"); do
		t="$(api GET "/servers/$SERVER_ID/tasks/$id?wait=15")"
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
	t="$(api GET "/servers/$SERVER_ID/tasks/$RM?wait=15")"
	jq -e '.status == "succeeded" and .result.container_removed == true and .result.images_removed > 0' <<<"$t" >/dev/null || fail "app.remove: $t"
done
[ -z "$(sudo docker ps -aq --filter label=dev.osriva.app)" ] || fail "app containers left"
[ -z "$(sudo docker images -q --filter label=dev.osriva.app)" ] || fail "app images left"
kill "$HTTP_PID"

step "Websites: Caddy reverse proxy (internal CA), reload, port conflict, disable"
login # proxy and site changes need recent authentication
wait_task() { # wait_task TASK_ID -> final task JSON
	local t s i
	for i in $(seq 1 30); do
		t="$(api GET "/servers/$SERVER_ID/tasks/$1?wait=15")"
		s="$(jq -r .status <<<"$t")"
		[ "$s" = queued ] || [ "$s" = running ] || break
	done
	echo "$t"
}
put_proxy() { api PUT "/servers/$SERVER_ID/proxy" "$1" | jq -r .task.id; }
task docker.container.run '{"image":"nginx:1.27-alpine","name":"osriva-ci-upstream","ports":[{"host_port":18085,"container_port":80}]}' 9 | jq -e '.status == "succeeded"' >/dev/null || fail "upstream container"

# 443 belongs to the control plane on this machine: the agent must refuse, not break it
wait_task "$(put_proxy '{"enabled":true,"http_port":18880,"https_port":443}')" | jq -e '.status == "failed" and (.error | contains("443 is already in use"))' >/dev/null || fail "busy port accepted"
curl -fsS -o /dev/null "$PANEL/api/v1/meta" || fail "control plane disturbed"
P="$(wait_task "$(put_proxy '{"enabled":true,"http_port":18880,"https_port":18443,"acme_email":"ci@example.com"}')")"
jq -e '.status == "succeeded" and .result.running == true' <<<"$P" >/dev/null || fail "enable proxy: $P"
[ "$(sudo docker inspect -f '{{.HostConfig.NetworkMode}} {{.HostConfig.Privileged}} {{.HostConfig.CapDrop}}' osriva-proxy)" = "host false [ALL]" ] || fail "proxy container settings"
[ "$(sudo stat -c '%a' /var/lib/osriva-agent/proxy)" = 700 ] || fail "proxy directory permissions"
curl -s -m 3 http://127.0.0.1:2019/config/ >/dev/null && fail "Caddy admin API reachable over TCP"

SITE_ID="$(api POST "/servers/$SERVER_ID/sites" '{"domain":"ci.localhost","upstream_port":18085,"tls_mode":"internal"}' | tee /tmp/site.json | jq -r .site.id)"
wait_task "$(jq -r .task.id /tmp/site.json)" | jq -e '.status == "succeeded"' >/dev/null || fail "site apply"
wait_for 30 "HTTPS through the proxy" curl -fsSk --resolve ci.localhost:18443:127.0.0.1 https://ci.localhost:18443/ || fail "site not served over HTTPS"
curl -fsSk --resolve ci.localhost:18443:127.0.0.1 https://ci.localhost:18443/ | grep -q "Welcome to nginx" || fail "wrong upstream"
[ "$(curl -s -o /dev/null -w '%{http_code}' --resolve ci.localhost:18880:127.0.0.1 http://ci.localhost:18880/)" = 308 ] || fail "HTTP is not redirected to HTTPS"
has_cert() { task proxy.status '{}' | jq -e '[.result.certificates[] | select(.domain == "ci.localhost")] | length == 1'; }
wait_for 30 "certificate listed" has_cert || fail "certificate not reported"

PLAIN="$(api POST "/servers/$SERVER_ID/sites" '{"domain":"plain.localhost","upstream_port":18085,"tls_mode":"none"}')"
wait_task "$(jq -r .task.id <<<"$PLAIN")" | jq -e '.status == "succeeded"' >/dev/null || fail "plain site apply (reload)"
curl -fsS --resolve plain.localhost:18880:127.0.0.1 http://plain.localhost:18880/ | grep -q "Welcome to nginx" || fail "plain HTTP site"
api POST "/servers/$SERVER_ID/sites" '{"domain":"ci.localhost","upstream_port":18086,"tls_mode":"internal"}' >/dev/null 2>&1 && fail "duplicate domain accepted"

wait_task "$(api DELETE "/servers/$SERVER_ID/sites/$SITE_ID" | jq -r .task.id)" | jq -e '.status == "succeeded"' >/dev/null || fail "site delete"
curl -sk -m 5 -o /dev/null --resolve ci.localhost:18443:127.0.0.1 https://ci.localhost:18443/ -w '%{http_code}' | grep -q 200 && fail "deleted site still served"
wait_task "$(put_proxy '{"enabled":false,"http_port":18880,"https_port":18443}')" | jq -e '.status == "succeeded" and .result.installed == false' >/dev/null || fail "disable proxy"
sudo docker inspect osriva-proxy >/dev/null 2>&1 && fail "proxy container left"
curl -s -m 3 -o /dev/null http://127.0.0.1:18880/ && fail "port 18880 still served"
task docker.container.action '{"id":"osriva-ci-upstream","action":"stop"}' >/dev/null
task docker.container.action '{"id":"osriva-ci-upstream","action":"remove"}' >/dev/null

step "CMS: WordPress from the official images, WP-CLI install, checksums, update, remove"
login
WP="$(api POST "/servers/$SERVER_ID/cms/wordpress" '{"name":"ciwp","title":"OSRIVA CI blog","admin_user":"ciadmin","admin_email":"ci@example.com","host_port":18095}')"
WP_ID="$(jq -r .instance.id <<<"$WP")"
WP_PASS="$(jq -r .admin_password <<<"$WP")"
[ "${#WP_PASS}" -ge 16 ] || fail "no admin password returned"
INSTALL="$(wait_task "$(jq -r .task.id <<<"$WP")")"
jq -e '.status == "succeeded" and .result.ok == true' <<<"$INSTALL" >/dev/null || fail "WordPress install: $INSTALL"
FRONT="$(curl -fsS http://127.0.0.1:18095/)" || fail "front page not served"
grep -q "OSRIVA CI blog" <<<"$FRONT" || fail "front page"
LOGIN="$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' -b 'wordpress_test_cookie=WP%20Cookie%20check' \
	--data-urlencode 'log=ciadmin' --data-urlencode "pwd=$WP_PASS" -d 'testcookie=1' http://127.0.0.1:18095/wp-login.php)"
case "$LOGIN" in 302*wp-admin*) ;; *) fail "admin login with the generated password: $LOGIN" ;; esac
[ -z "$(sudo docker port osriva-wp-ciwp-db)" ] || fail "database port published"
[ "$(sudo docker inspect -f '{{(index (index .HostConfig.PortBindings "80/tcp") 0).HostIp}}' osriva-wp-ciwp)" = 127.0.0.1 ] || fail "WordPress not on loopback"

INFO="$(task cms.wp.info '{"name":"ciwp"}')"
jq -e '.status == "succeeded" and .result.installed == true and (.result.core_version | startswith("6.")) and ([.result.plugins[] | select(.name == "akismet")] | length == 1) and .result.site_url == "http://127.0.0.1:18095"' <<<"$INFO" >/dev/null || fail "info: $INFO"
VERIFY="$(task cms.wp.verify '{"name":"ciwp"}' 6)"
jq -e '.status == "succeeded" and .result.ok == true' <<<"$VERIFY" >/dev/null || fail "checksum verification: $VERIFY"
task cms.wp.update '{"name":"ciwp","target":"plugins"}' 12 | jq -e '.status == "succeeded"' >/dev/null || fail "plugin update"
[ "$(pg "SELECT count(*) FROM agent_tasks WHERE params::text LIKE '%$WP_PASS%'")" = 0 ] || fail "admin password kept in agent_tasks"
[ "$(pg "SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%$WP_PASS%'")" = 0 ] || fail "admin password in the audit log"

wait_task "$(api DELETE "/servers/$SERVER_ID/cms/$WP_ID" | jq -r .id)" | jq -e '.status == "succeeded" and .result.data_deleted == false' >/dev/null || fail "remove keeping data"
sudo docker inspect osriva-wp-ciwp >/dev/null 2>&1 && fail "WordPress container left"
sudo docker volume inspect osriva-wp-ciwp-db >/dev/null || fail "data volume removed although it should be kept"
task cms.remove '{"name":"ciwp","delete_data":true}' | jq -e '.status == "succeeded" and .result.data_deleted == true' >/dev/null || fail "remove data"
sudo docker volume inspect osriva-wp-ciwp-db >/dev/null 2>&1 && fail "data volume left"

step "Backups: encrypted volume backup, verify, restore, damaged archive refused"
login
sudo docker volume create osriva-ci-vol >/dev/null
VOLDIR="$(sudo docker volume inspect -f '{{.Mountpoint}}' osriva-ci-vol)"
echo "v1 data" | sudo tee "$VOLDIR/data.txt" >/dev/null
sudo mkdir "$VOLDIR/sub" && sudo ln -s ../data.txt "$VOLDIR/sub/link"
sudo chown 1234:1234 "$VOLDIR/data.txt" && sudo chmod 640 "$VOLDIR/data.txt"
B="$(api POST "/servers/$SERVER_ID/backups" '{"kind":"volume","target":"osriva-ci-vol"}')"
BID="$(jq -r .id <<<"$B")"
wait_task "$(jq -r .task_id <<<"$B")" | jq -e '.status == "succeeded" and .result.files >= 3' >/dev/null || fail "volume backup"
BFILE="/var/lib/osriva-agent/backups/$BID.osb"
[ "$(sudo stat -c '%a' "$BFILE")" = 600 ] || fail "backup file mode"
sudo grep -q "v1 data" "$BFILE" && fail "backup is not encrypted"
api GET "/servers/$SERVER_ID/backups" | jq -e --arg id "$BID" '[.backups[] | select(.id == $id and (.sha256 | length) == 64)] | length == 1' >/dev/null || fail "checksum not recorded"
wait_task "$(api POST "/servers/$SERVER_ID/backups/$BID/verify" | jq -r .id)" | jq -e '.status == "succeeded" and .result.ok == true' >/dev/null || fail "verify"

echo "changed" | sudo tee "$VOLDIR/data.txt" >/dev/null
sudo touch "$VOLDIR/junk"
wait_task "$(api POST "/servers/$SERVER_ID/backups/$BID/restore" | jq -r .id)" | jq -e '.status == "succeeded"' >/dev/null || fail "restore"
[ "$(sudo cat "$VOLDIR/data.txt")" = "v1 data" ] || fail "content not restored"
[ "$(sudo stat -c '%u:%g %a' "$VOLDIR/data.txt")" = "1234:1234 640" ] || fail "owner or mode not restored"
[ "$(sudo readlink "$VOLDIR/sub/link")" = ../data.txt ] || fail "symlink not restored"
sudo test -e "$VOLDIR/junk" && fail "files created after the backup survived the restore"

# a damaged archive is detected and the live data is not touched
sudo dd if=/dev/urandom of="$BFILE" bs=1 seek=200 count=8 conv=notrunc status=none
echo "live" | sudo tee "$VOLDIR/data.txt" >/dev/null
wait_task "$(api POST "/servers/$SERVER_ID/backups/$BID/verify" | jq -r .id)" | jq -e '.result.ok == false and (.result.problem | contains("damaged"))' >/dev/null || fail "damage not detected"
wait_task "$(api POST "/servers/$SERVER_ID/backups/$BID/restore" | jq -r .id)" | jq -e '.status == "failed" and (.error | contains("nothing was changed"))' >/dev/null || fail "damaged backup restored"
[ "$(sudo cat "$VOLDIR/data.txt")" = live ] || fail "live data touched by a refused restore"
api POST "/servers/$SERVER_ID/backups/recovery-key" | jq -e '.recovery_key | startswith("AGE-SECRET-KEY-1")' >/dev/null || fail "recovery key"
[ "$(pg "SELECT count(*) FROM agent_tasks WHERE params::text LIKE '%AGE-SECRET-KEY%'")" = 0 ] || fail "backup key kept in agent_tasks"
[ "$(pg "SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%AGE-SECRET-KEY%'")" = 0 ] || fail "backup key in the audit log"
wait_task "$(api DELETE "/servers/$SERVER_ID/backups/$BID" | jq -r .id)" | jq -e '.status == "succeeded"' >/dev/null || fail "delete backup"
sudo test -e "$BFILE" && fail "backup file left"

step "Backups: WordPress database + files, restore after changes; scheduled backup"
WP="$(api POST "/servers/$SERVER_ID/cms/wordpress" '{"name":"cibk","title":"OSRIVA backup blog","admin_user":"ciadmin","admin_email":"ci@example.com","host_port":18097}')"
WP_ID="$(jq -r .instance.id <<<"$WP")"
wait_task "$(jq -r .task.id <<<"$WP")" | jq -e '.status == "succeeded"' >/dev/null || fail "WordPress for backups"
B="$(api POST "/servers/$SERVER_ID/backups" '{"kind":"wordpress","target":"cibk"}')"
BID="$(jq -r .id <<<"$B")"
wait_task "$(jq -r .task_id <<<"$B")" | jq -e '.status == "succeeded"' >/dev/null || fail "WordPress backup"
DBPW="$(sudo docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' osriva-wp-cibk | sed -n 's/^WORDPRESS_DB_PASSWORD=//p')"
sudo docker exec -e MYSQL_PWD="$DBPW" osriva-wp-cibk-db mariadb -uwordpress wordpress -e "UPDATE wp_options SET option_value='Defaced' WHERE option_name='blogname'"
sudo docker exec osriva-wp-cibk sh -c 'echo marker > /var/www/html/osriva-marker.txt'
PAGE="$(curl -fsS http://127.0.0.1:18097/)"; grep -q Defaced <<<"$PAGE" || fail "test change not visible"
wait_task "$(api POST "/servers/$SERVER_ID/backups/$BID/restore" | jq -r .id)" | jq -e '.status == "succeeded" and .result.database == true and (.result.stopped | length) == 1' >/dev/null || fail "WordPress restore"
wait_for 60 "WordPress back" curl -fsS -o /dev/null http://127.0.0.1:18097/ || fail "WordPress down after restore"
PAGE="$(curl -fsS http://127.0.0.1:18097/)"; grep -q "OSRIVA backup blog" <<<"$PAGE" || fail "database not restored"
sudo docker exec osriva-wp-cibk test -e /var/www/html/osriva-marker.txt && fail "files not restored (marker still there)"
sudo docker exec osriva-wp-cibk test -f /var/www/html/wp-config.php || fail "wp-config.php missing after restore"

POL="$(api POST "/servers/$SERVER_ID/backup-policies" '{"kind":"volume","target":"osriva-ci-vol","interval_hours":24,"keep":2}' | jq -r .id)"
has_scheduled() { api GET "/servers/$SERVER_ID/backups" | jq -e --arg p "$POL" '[.backups[] | select(.policy_id == $p and .status == "succeeded")] | length == 1'; }
wait_for 120 "scheduled backup (janitor runs every 30 s)" has_scheduled || fail "scheduled backup did not run"
api DELETE "/servers/$SERVER_ID/backup-policies/$POL" >/dev/null
wait_task "$(api DELETE "/servers/$SERVER_ID/cms/$WP_ID?delete_data=true" | jq -r .id)" >/dev/null
sudo docker volume rm osriva-ci-vol >/dev/null

step "Alerts: signed webhook + e-mail (Mailpit), CPU / offline rules, rollups, audit retention"
login
HOOKS="$(mktemp)"
python3 "$(dirname "$0")/webhook-receiver.py" 18099 "$HOOKS" &
HOOK_PID=$!
GW="$(sudo docker network inspect osriva_public -f '{{(index .IPAM.Config 0).Gateway}}')"
sudo docker run -d --name ci-mail --network osriva_public -p 127.0.0.1:18025:8025 axllent/mailpit:latest >/dev/null
wait_for 60 "Mailpit" curl -fsS -o /dev/null http://127.0.0.1:18025/api/v1/messages || fail "Mailpit"

WH="$(api POST /alerts/channels "$(jq -nc --arg u "http://$GW:18099/hook/T0KEN" '{name:"ci hook",kind:"webhook",enabled:true,config:{url:$u}}')")"
WH_ID="$(jq -r .channel.id <<<"$WH")"; WH_SECRET="$(jq -r .webhook_secret <<<"$WH")"
[ "${#WH_SECRET}" = 64 ] || fail "webhook secret not returned"
EM_ID="$(api POST /alerts/channels '{"name":"ci mail","kind":"email","enabled":true,"config":{"host":"ci-mail","port":1025,"tls":"none","from":"osriva@example.com","to":["ops@example.com"]}}' | jq -r .channel.id)"
api POST "/alerts/channels/$WH_ID/test" | jq -e '.delivered == 1' >/dev/null || fail "webhook test delivery"
api POST "/alerts/channels/$EM_ID/test" | jq -e '.delivered == 1' >/dev/null || fail "e-mail test delivery"
verify_hook() { # verify_hook LINE: the signature must match HMAC-SHA256(secret, timestamp "." body)
	local ts sig body want
	ts="$(jq -r '.headers["X-Osriva-Timestamp"]' <<<"$1")"
	sig="$(jq -r '.headers["X-Osriva-Signature"]' <<<"$1")"
	want="sha256=$( (printf '%s.' "$ts"; jq -r .body <<<"$1" | base64 -d) | openssl dgst -sha256 -hmac "$WH_SECRET" -r | cut -d' ' -f1)"
	[ "$sig" = "$want" ]
}
verify_hook "$(head -n1 "$HOOKS")" || fail "webhook signature does not verify"
jq -e 'select(.path == "/hook/T0KEN") | .headers["X-Osriva-Event"] == "alert.test"' "$HOOKS" >/dev/null || fail "test webhook"
curl -fsS 'http://127.0.0.1:18025/api/v1/search?query=subject:Test' | jq -e '.messages_count >= 1' >/dev/null || fail "test e-mail not received"
[ "$(pg "SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%T0KEN%' OR metadata::text LIKE '%$WH_SECRET%'")" = 0 ] || fail "webhook secret or path in the audit log"

# CPU above 0.1% for one minute fires on any live machine
api POST /alerts/rules "$(jq -nc --arg s "$SERVER_ID" --arg w "$WH_ID" --arg e "$EM_ID" '{name:"ci cpu",server_id:$s,metric:"cpu",threshold:0.1,duration_minutes:1,channel_ids:[$w,$e],enabled:true}')" >/dev/null
cpu_fired() { grep -q '"alert.firing"' "$HOOKS" && curl -fsS 'http://127.0.0.1:18025/api/v1/search?query=subject:FIRING' | jq -e '.messages_count >= 1'; }
wait_for 150 "CPU alert on webhook and e-mail" cpu_fired || fail "CPU alert not delivered"
while read -r line; do verify_hook "$line" || fail "a webhook signature did not verify"; done <"$HOOKS"

# offline: stop the agent, expect firing, start it, expect resolved
api POST /alerts/rules "$(jq -nc --arg s "$SERVER_ID" --arg w "$WH_ID" '{name:"ci offline",server_id:$s,metric:"offline",threshold:1,duration_minutes:1,channel_ids:[$w],enabled:true}')" >/dev/null
sudo systemctl stop osriva-agent
offline_fired() { api GET "/alerts/events?limit=50" | jq -e '[.events[] | select(.rule_name == "ci offline" and .state == "firing")] | length == 1'; }
wait_for 240 "offline alert" offline_fired || fail "offline alert did not fire"
sudo systemctl start osriva-agent
offline_resolved() { api GET "/alerts/events?limit=50" | jq -e '[.events[] | select(.rule_name == "ci offline" and .state == "resolved")] | length == 1'; }
wait_for 180 "offline resolved" offline_resolved || fail "offline alert did not resolve"

# retention and rollups run when the API starts: seed an old audit entry and samples two hours back, then restart it
sudo pkill -f webhook-receiver.py || true
pg "INSERT INTO audit_logs (organization_id, action, outcome, created_at) VALUES ('00000000-0000-4000-8000-000000000001', 'ci.ancient', 'success', now() - interval '40 days')" >/dev/null
pg "INSERT INTO metric_samples (server_id, ts, data) SELECT '$SERVER_ID', t, jsonb_build_object('ts', to_char(t AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'), 'cpu_percent', 42) FROM (SELECT date_trunc('hour', now()) - interval '2 hours' + g * interval '5 minutes' AS t FROM generate_series(0, 5) g) s ON CONFLICT DO NOTHING" >/dev/null
api PUT /settings/audit-retention '{"days":30}' | jq -e '.audit_retention_days == 30' >/dev/null || fail "set audit retention"
sudo docker compose --project-name osriva --env-file /etc/osriva/osriva.env -f /opt/osriva/current/docker-compose.yml restart api >/dev/null
wait_for 60 "API back" curl -fsS -o /dev/null "$PANEL/api/v1/meta" || fail "API did not come back"
login
pruned() { [ "$(pg "SELECT count(*) FROM audit_logs WHERE action = 'ci.ancient'")" = 0 ] && [ "$(pg "SELECT count(*) FROM audit_logs WHERE action = 'audit.pruned'")" -ge 1 ]; }
wait_for 60 "audit retention" pruned || {
	pg "SELECT name, holder, expires_at, now() FROM leases"
	sudo docker compose --project-name osriva --env-file /etc/osriva/osriva.env -f /opt/osriva/current/docker-compose.yml logs --tail 60 api | grep -iE "lease|shutting|retention|audit|warn|error" || true
	fail "old audit entry not pruned"
}
has_rollup() { api GET "/servers/$SERVER_ID/metrics?minutes=10080" | jq -e '.resolution == "hour" and ([.samples[] | select(.cpu_percent == 42 and .samples == 6)] | length == 1)'; }
wait_for 60 "hourly rollup" has_rollup || fail "rollup not written"
wait_for 90 "server online again" is_online || fail "agent did not reconnect"
sudo docker rm -f ci-mail >/dev/null

step "Plugins: a signed notifier mounted into the API container, approved, delivering; a changed binary stops"
sudo test -d /etc/osriva/plugins || fail "the installer did not create /etc/osriva/plugins"
api GET /plugins | jq -e '.enabled == false' >/dev/null || fail "plugins enabled without trusted keys"
PLUGHOOKS="$(mktemp)"
python3 "$(dirname "$0")/webhook-receiver.py" 18096 "$PLUGHOOKS" &
PLUGHOOK_PID=$!
PL="$(mktemp -d)"; mkdir -p "$PL/chat-notifier"
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$PL/osriva-plugin" ./pkg/pluginsdk/cmd/osriva-plugin &&
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$PL/chat-notifier/chat-notifier" ./pkg/pluginsdk/examples/chat-notifier)
printf '{"name":"chat-notifier","version":"0.1.0","publisher":"ci","description":"system test","protocol":1,"kinds":["notifier"],"executables":{"linux/amd64":{"path":"chat-notifier","sha256":"%064d"}}}' 0 >"$PL/chat-notifier/plugin.json"
PUB="$("$PL/osriva-plugin" keygen --out "$PL/publisher.key" | awk '/public key/ {print $3}')"
"$PL/osriva-plugin" sign --key "$PL/publisher.key" "$PL/chat-notifier"
sudo cp -r "$PL/chat-notifier" /etc/osriva/plugins/
sudo chown -R root:root /etc/osriva/plugins/chat-notifier
echo "OSRIVA_PLUGIN_TRUSTED_KEYS=$PUB" | sudo tee -a /etc/osriva/osriva.env >/dev/null
sudo "$INSTALLER" repair --yes >/dev/null
login
api GET /plugins | jq -e '.enabled and ([.plugins[] | select(.name == "chat-notifier" and .valid and (.enabled | not))] | length == 1)' >/dev/null || fail "plugin not listed as valid"
api POST /auth/reauth "$(jq -nc --arg p "$PASSWORD" '{password:$p}')" >/dev/null || fail "re-auth"
api PUT /plugins/chat-notifier '{"enabled":true}' | jq -e '.runnable' >/dev/null || fail "approve plugin"
PC_ID="$(api POST /alerts/channels "$(jq -nc --arg u "http://$GW:18096/hook/PLUGIN" '{name:"ci plugin",kind:"plugin",enabled:true,config:{plugin:"chat-notifier",settings:{url:$u}}}')" | jq -r .channel.id)"
PT="$(api POST "/alerts/channels/$PC_ID/test")"
jq -e '.delivered == 1' <<<"$PT" >/dev/null || {
	echo "$PT"
	api GET /plugins
	sudo ls -la /etc/osriva/plugins /etc/osriva/plugins/chat-notifier
	sudo docker compose --project-name osriva --env-file /etc/osriva/osriva.env -f /opt/osriva/current/docker-compose.yml logs --tail 30 api | grep -i plugin || true
	fail "plugin test delivery"
}
jq -se '[.[] | select(.path == "/hook/PLUGIN") | .body | @base64d | fromjson | .text | select(contains("Test notification"))] | length == 1' "$PLUGHOOKS" >/dev/null || fail "the plugin's message did not arrive"
# One byte appended to the approved binary: it must not run any more.
echo x | sudo tee -a /etc/osriva/plugins/chat-notifier/chat-notifier >/dev/null
api POST /plugins/rescan >/dev/null
! api POST "/alerts/channels/$PC_ID/test" 2>/dev/null | jq -e '.delivered == 1' >/dev/null || fail 'a changed plugin binary still ran'
api DELETE "/alerts/channels/$PC_ID" >/dev/null
kill "$PLUGHOOK_PID" 2>/dev/null || true

step "Fleet: server groups, a batch across the group, queued notification retries, leases"
login
PENDING_ID="$(api POST /servers '{"name":"ci-pending"}' | jq -r .server.id)"
GROUP_ID="$(api POST /server-groups '{"name":"ci fleet","description":"system test"}' | jq -r .id)"
api PUT "/server-groups/$GROUP_ID/members" "$(jq -nc --arg a "$SERVER_ID" --arg b "$PENDING_ID" '{server_ids:[$a,$b]}')" | jq -e '.server_ids | length == 2' >/dev/null || fail "group members"
! api POST /fleet/batches "$(jq -nc --arg g "$GROUP_ID" '{type:"files.read",params:{path:"/etc/hostname"},group_id:$g}')" >/dev/null 2>&1 || fail 'file tasks must not run as a batch'
BATCH="$(api POST /fleet/batches "$(jq -nc --arg g "$GROUP_ID" '{type:"system.time",params:{},group_id:$g}')")"
jq -e '(.tasks | length) == 1 and (.skipped | length) == 1 and .skipped[0].reason == "agent not enrolled"' <<<"$BATCH" >/dev/null || fail "batch targets: $BATCH"
BATCH_ID="$(jq -r .id <<<"$BATCH")"
batch_done() { api GET "/fleet/batches/$BATCH_ID" | jq -e '.counts.succeeded == 1'; }
wait_for 60 "batch task succeeded on the agent" batch_done || fail "batch did not complete"
TASK_ID="$(api GET "/fleet/batches/$BATCH_ID" | jq -r .tasks[0].task_id)"
api GET "/servers/$SERVER_ID/tasks/$TASK_ID" | jq -e --arg b "$BATCH_ID" '.batch_id == $b and (.result.timezone | length > 0)' >/dev/null || fail "batch task result"
[ "$(pg "SELECT count(*) FROM audit_logs WHERE action = 'fleet.batch'")" = 1 ] || fail "fleet.batch audit entry"

# A webhook that answers 503 twice: the job queue retries (30 s, then 60 s) and the third attempt is delivered.
FLAKY="$(mktemp)"
python3 "$(dirname "$0")/webhook-receiver.py" 18098 "$FLAKY" 2 &
FLAKY_PID=$!
FL_ID="$(api POST /alerts/channels "$(jq -nc --arg u "http://$GW:18098/flaky" '{name:"ci flaky",kind:"webhook",enabled:true,config:{url:$u}}')" | jq -r .channel.id)"
RULE_ID="$(api POST /alerts/rules "$(jq -nc --arg g "$GROUP_ID" --arg c "$FL_ID" '{name:"ci group cpu",group_id:$g,metric:"cpu",threshold:0.1,duration_minutes:1,channel_ids:[$c],enabled:true}')" | jq -r .id)"
retrying() { api GET "/alerts/events?limit=50" | jq -e '[.events[] | select(.rule_name == "ci group cpu" and .state == "firing" and .pending == 1)] | length == 1'; }
wait_for 120 "group alert queued and retrying" retrying || fail "group alert not retrying"
delivered() { api GET "/alerts/events?limit=50" | jq -e '[.events[] | select(.rule_name == "ci group cpu" and .delivered == 1 and .pending == 0)] | length == 1'; }
wait_for 180 "delivered on the third attempt" delivered || fail "retried notification not delivered"
[ "$(jq -s '[.[] | .status] | map(tostring) | join(",")' -r "$FLAKY")" = "503,503,204" ] || fail "attempts: $(jq -c .status "$FLAKY")"
[ "$(jq -rs 'map(.headers["X-Osriva-Delivery"]) | unique | length' "$FLAKY")" = 1 ] || fail "delivery id changed between retries"
[ "$(pg "SELECT attempts FROM jobs WHERE kind = 'notification.deliver' AND status = 'succeeded' ORDER BY updated_at DESC LIMIT 1")" = 3 ] || fail "job attempts"
api GET /jobs | jq -e '.counts.succeeded >= 1' >/dev/null || fail "jobs endpoint"
kill "$FLAKY_PID" || true
[ "$(pg "SELECT string_agg(name, ',' ORDER BY name) FROM leases")" = "janitor,monitor,provisioning" ] || fail "loop leases"

# A group used by an alert rule cannot be deleted.
! api DELETE "/server-groups/$GROUP_ID" >/dev/null 2>&1 || fail 'deleted a group used by a rule'
api DELETE "/alerts/rules/$RULE_ID" >/dev/null
api DELETE "/server-groups/$GROUP_ID" >/dev/null || fail "delete group"

step "Provider Mode: libvirt/KVM node, IP pool, Ubuntu cloud image, a real VPS reached over SSH, lifecycle, delete"
# This disposable CI runner becomes a KVM host; on real hosts the agent never installs anything.
[ -e /dev/kvm ] || fail "this runner has no /dev/kvm"
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq qemu-kvm libvirt-daemon-system genisoimage >/dev/null
sudo systemctl start libvirtd
sudo virsh -c qemu:///system net-info default >/dev/null 2>&1 || fail "libvirt default network missing"
sudo virsh -c qemu:///system net-start default >/dev/null 2>&1 || true
login
host_ready() { task vm.host '{}' | jq -e '.status == "succeeded" and .result.available and .result.kvm and ([.result.networks[] | select(.name == "default" and .active)] | length == 1)'; }
wait_for 60 "agent sees libvirt and KVM" host_ready || { task vm.host '{}'; fail "vm.host"; }
api PUT /settings/panel-mode '{"mode":"provider"}' | jq -e '.panel_mode == "provider"' >/dev/null || fail "Provider Mode"
DC_ID="$(api POST /provider/datacenters '{"name":"ci-dc","location":"GitHub Actions"}' | jq -r .id)"
NODE_ID="$(api POST /provider/nodes "$(jq -nc --arg s "$SERVER_ID" --arg d "$DC_ID" '{server_id:$s,datacenter_id:$d,network:"default"}')" | jq -r .id)"
[ -n "$NODE_ID" ] && [ "$NODE_ID" != null ] || fail "node"
POOL_ID="$(api POST /provider/ip-pools "$(jq -nc --arg d "$DC_ID" '{name:"ci-nat",datacenter_id:$d,cidr:"192.168.122.0/24",gateway:"192.168.122.1",first:"192.168.122.100",last:"192.168.122.110",dns:["192.168.122.1"]}')" | jq -r .id)"
IMG_BASE=https://cloud-images.ubuntu.com/minimal/releases/noble/release
IMG_SHA="$(curl -fsS "$IMG_BASE/SHA256SUMS" | awk '$2 == "*ubuntu-24.04-minimal-cloudimg-amd64.img" || $2 == "ubuntu-24.04-minimal-cloudimg-amd64.img" {print $1}')"
[ "${#IMG_SHA}" = 64 ] || fail "image checksum not found in the publisher's SHA256SUMS"
TPL_ID="$(api POST /provider/templates "$(jq -nc --arg u "$IMG_BASE/ubuntu-24.04-minimal-cloudimg-amd64.img" --arg h "$IMG_SHA" '{name:"ubuntu-24.04-minimal",image_url:$u,image_sha256:$h,min_disk_gb:5,default_user:"ubuntu"}')" | jq -r .id)"
VMKEY="$(mktemp -d)/id_ed25519"
ssh-keygen -q -t ed25519 -N '' -f "$VMKEY"
VPS="$(api POST /provider/vps "$(jq -nc --arg n "$NODE_ID" --arg t "$TPL_ID" --arg p "$POOL_ID" --arg k "$(cat "$VMKEY.pub")" '{name:"ci-vm-1",node_id:$n,template_id:$t,pool_id:$p,vcpus:1,memory_mb:1024,disk_gb:6,ssh_keys:[$k]}')")"
VPS_ID="$(jq -r .id <<<"$VPS")"; VPS_IP="$(jq -r .ip_address <<<"$VPS")"
[ "$VPS_IP" = 192.168.122.100 ] || fail "address: $VPS"
vps_state() { api GET "/provider/vps/$VPS_ID" | jq -r '.state + "/" + (.pending_op // "")'; }
vps_is() { [ "$(vps_state)" = "$1/" ]; }
wait_for 900 "VPS running (image download, disk, cloud-init seed, boot)" vps_is running || { api GET "/provider/vps/$VPS_ID"; fail "VPS not running"; }
sudo virsh -c qemu:///system dominfo "osriva-$VPS_ID" | grep -q 'State: *running' || fail "domain not running"
sudo test -f "/var/lib/libvirt/images/osriva/$VPS_ID.qcow2" || fail "disk missing"
vm_ssh() { ssh -i "$VMKEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o BatchMode=yes "ubuntu@$VPS_IP" "$@"; }
vm_diag() {
	sudo tail -n 80 "/var/log/libvirt/qemu/osriva-$VPS_ID-console.log" || true
	ip -4 addr show virbr0 || true; ping -c 2 -W 2 "$VPS_IP" || true; ip neigh show dev virbr0 || true
	sudo iptables -S | grep -iE "virbr|FORWARD|INPUT" | head -30 || true
	sudo virsh -c qemu:///system dumpxml "osriva-$VPS_ID" | grep -E "interface|mac|source|serial|log" || true
}
wait_for 300 "SSH into the VPS with the injected key" vm_ssh true || { vm_diag; fail "no SSH"; }
[ "$(vm_ssh hostname)" = ci-vm-1 ] || fail "hostname from cloud-init"
vm_ssh "ip -4 -o addr show" | grep -q "192.168.122.100/24" || fail "static address from cloud-init"
! ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o BatchMode=yes -o PubkeyAuthentication=no "ubuntu@$VPS_IP" true 2>/dev/null || fail 'password login must be disabled'
# lifecycle
api POST "/provider/vps/$VPS_ID/actions" '{"action":"shutdown"}' >/dev/null
wait_for 240 "VPS stopped after a graceful shutdown" vps_is stopped || fail "shutdown"
api POST "/provider/vps/$VPS_ID/actions" '{"action":"start"}' >/dev/null
wait_for 120 "VPS running again" vps_is running || fail "start"
wait_for 240 "SSH after the restart" vm_ssh true || fail "no SSH after start"
api POST /provider/vps "$(jq -nc --arg n "$NODE_ID" --arg t "$TPL_ID" --arg p "$POOL_ID" --arg k "$(cat "$VMKEY.pub")" '{name:"ci-vm-big",node_id:$n,template_id:$t,pool_id:$p,vcpus:64,memory_mb:1024,disk_gb:6,ssh_keys:[$k]}')" >/dev/null 2>&1 && fail "a VPS beyond the node's capacity was accepted"
# delete: typed name, the domain and its disk are gone, the address returns to the pool
login # re-authentication: the boot above can outlast the 10-minute window
! api DELETE "/provider/vps/$VPS_ID" '{"confirm":"wrong"}' >/dev/null 2>&1 || fail 'delete without the typed name'
api DELETE "/provider/vps/$VPS_ID" '{"confirm":"ci-vm-1"}' >/dev/null || fail "delete"
vps_gone() { ! api GET "/provider/vps/$VPS_ID" >/dev/null 2>&1; }
wait_for 180 "VPS deleted" vps_gone || fail "VPS not deleted"
! sudo virsh -c qemu:///system dominfo "osriva-$VPS_ID" >/dev/null 2>&1 || fail 'domain still defined'
! sudo test -e "/var/lib/libvirt/images/osriva/$VPS_ID.qcow2" || fail 'disk still present'
api GET /provider/ip-pools | jq -e '.pools[0].allocated | length == 0' >/dev/null || fail "address not released"
sudo virsh -c qemu:///system net-info default | grep -q 'Active: *yes' || fail "the libvirt network must be untouched"
[ "$(pg "SELECT count(*) FROM audit_logs WHERE action LIKE 'provider.vps_%'")" -ge 4 ] || fail "provider audit entries"
# ---- Phase 11: a billing system provisions a VPS through the signed API ----
login # re-authentication for creating the API client
BILLHOOKS="$(mktemp)"
python3 "$(dirname "$0")/webhook-receiver.py" 18097 "$BILLHOOKS" &
BILL_PID=$!
PLAN_ID="$(api POST /provider/plans "$(jq -nc --arg t "$TPL_ID" --arg p "$POOL_ID" '{name:"ci-small",template_id:$t,pool_id:$p,vcpus:1,memory_mb:1024,disk_gb:6}')" | jq -r .id)"
CL="$(api POST /api-clients "$(jq -nc --arg u "http://$GW:18097/billing" '{name:"ci-billing",scopes:["provisioning.read","provisioning.write"],webhook_url:$u}')")"
CL_KEY="$(jq -r .client.key_id <<<"$CL")"; CL_SECRET="$(jq -r .secret <<<"$CL")"
[ "${#CL_SECRET}" = 64 ] || fail "API client secret"
SIGNED_BODY="$(mktemp)"
signed() { # signed METHOD PATH [BODY] [IDEMPOTENCY_KEY] [NONCE] -> HTTP status; body in $SIGNED_BODY
	local m="$1" p="$2" b="${3:-}" k="${4:-}" n="${5:-$(openssl rand -hex 12)}" ts sig
	ts="$(date +%s)"
	sig="sha256=$(printf '%s\n%s\n%s\n%s\n%s' "$m" "$p" "$ts" "$n" "$(printf '%s' "$b" | sha256sum | cut -d' ' -f1)" | openssl dgst -sha256 -hmac "$CL_SECRET" -r | cut -d' ' -f1)"
	local args=(-sS -o "$SIGNED_BODY" -w '%{http_code}' -X "$m" "$PANEL$p" -H "X-Osriva-Key: $CL_KEY" -H "X-Osriva-Timestamp: $ts"
		-H "X-Osriva-Nonce: $n" -H "X-Osriva-Signature: $sig")
	[ -n "$b" ] && args+=(-H "Content-Type: application/json" -d "$b")
	[ -n "$k" ] && args+=(-H "Idempotency-Key: $k")
	curl "${args[@]}"
}
[ "$(signed GET /api/v1/provisioning/plans)" = 200 ] && jq -e '.plans | length == 1' "$SIGNED_BODY" >/dev/null || fail "signed plans"
REPLAY_NONCE="$(openssl rand -hex 12)"
[ "$(signed GET /api/v1/provisioning/plans '' '' "$REPLAY_NONCE")" = 200 ] || fail "signed request"
[ "$(signed GET /api/v1/provisioning/plans '' '' "$REPLAY_NONCE")" = 401 ] || fail "a reused nonce must be refused"
[ "$(curl -s -o /dev/null -w '%{http_code}' "$PANEL/api/v1/provisioning/plans")" = 401 ] || fail "unsigned request"
# Phase 12: a Customer Panel account receives the billed VPS.
CUST="$(api POST /customers '{"email":"ci-customer@example.com","name":"CI Customer"}')"
CUST_TOKEN="$(jq -r .setup_path <<<"$CUST" | sed 's/.*#//')"
CJAR="$(mktemp)"
cust() { # cust METHOD PATH [JSON] (customer cookie jar; CSRF from /customer/auth/me)
	local args=(-sS -o "$SIGNED_BODY" -w '%{http_code}' -b "$CJAR" -c "$CJAR" -X "$1" "$PANEL/api/v1/customer$2" -H "X-CSRF-Token: ${CUST_CSRF:-}")
	[ $# -ge 3 ] && args+=(-H "Content-Type: application/json" -d "$3")
	curl "${args[@]}"
}
[ "$(cust POST /auth/setup "$(jq -nc --arg t "$CUST_TOKEN" '{token:$t,password:"ci customer password 1"}')")" = 204 ] || fail "customer setup"
[ "$(cust POST /auth/setup "$(jq -nc --arg t "$CUST_TOKEN" '{token:$t,password:"ci customer password 2"}')")" = 409 ] || fail "a setup link must work once"
SVC_BODY="$(jq -nc --arg p "$PLAN_ID" --arg k "$(cat "$VMKEY.pub")" '{plan_id:$p,external_id:"ci-order-1",hostname:"ci-billed-1",ssh_keys:[$k],customer_email:"ci-customer@example.com"}')"
[ "$(signed POST /api/v1/provisioning/services "$SVC_BODY" ci-create-order-1)" = 202 ] || { cat "$SIGNED_BODY"; fail "provision"; }
SVC_ID="$(jq -r .id "$SIGNED_BODY")"
[ "$(signed POST /api/v1/provisioning/services "$SVC_BODY" ci-create-order-1)" = 202 ] && [ "$(jq -r .id "$SIGNED_BODY")" = "$SVC_ID" ] || fail "idempotent retry"
[ "$(signed POST /api/v1/provisioning/services "$SVC_BODY" ci-create-order-1-again)" = 200 ] && [ "$(jq -r .id "$SIGNED_BODY")" = "$SVC_ID" ] || fail "same external id"
svc_status() { signed GET "/api/v1/provisioning/services/$SVC_ID" >/dev/null; jq -r .status "$SIGNED_BODY"; }
svc_is() { [ "$(svc_status)" = "$1" ]; }
wait_for 300 "billed service active (image already cached)" svc_is active || { cat "$SIGNED_BODY"; fail "service not active"; }
BILLED_IP="$(jq -r .ip_address "$SIGNED_BODY")"
[ "$BILLED_IP" = 192.168.122.100 ] || fail "billed service address: $BILLED_IP"
hook_has() { jq -e --arg e "$1" 'select(.headers["X-Osriva-Event"] == $e)' "$BILLHOOKS" >/dev/null 2>&1; }
wait_for 60 "service.active webhook" hook_has service.active || fail "no service.active webhook"
verify_bill() { local ts sig want
	ts="$(jq -r '.headers["X-Osriva-Timestamp"]' <<<"$1")"; sig="$(jq -r '.headers["X-Osriva-Signature"]' <<<"$1")"
	want="sha256=$( (printf '%s.' "$ts"; jq -r .body <<<"$1" | base64 -d) | openssl dgst -sha256 -hmac "$CL_SECRET" -r | cut -d' ' -f1)"
	[ "$sig" = "$want" ]; }
verify_bill "$(head -n1 "$BILLHOOKS")" || fail "billing webhook signature"
[ "$(cust POST /auth/login '{"email":"ci-customer@example.com","password":"ci customer password 1"}')" = 200 ] || fail "customer login"
CUST_CSRF="$(jq -r .csrf_token "$SIGNED_BODY")"
grep -q "/api/v1/customer" "$CJAR" || fail "customer cookie path"
[ "$(cust GET /vps)" = 200 ] && jq -e --arg ip "$BILLED_IP" '.vps | length == 1 and .[0].ip_address == $ip and (.[0] | has("node_id") | not)' "$SIGNED_BODY" >/dev/null || { cat "$SIGNED_BODY"; fail "customer sees their VPS only"; }
CUST_VPS="$(jq -r '.vps[0].id' "$SIGNED_BODY")"
[ "$(cust POST "/vps/$CUST_VPS/actions" '{"action":"stop"}')" = 400 ] || fail "force stop is not for customers"
[ "$(cust POST "/vps/$CUST_VPS/actions" '{"action":"reboot"}')" = 202 ] || { cat "$SIGNED_BODY"; fail "customer reboot"; }
cust_running() { cust GET "/vps/$CUST_VPS" >/dev/null; jq -e '.state == "running" and .pending == ""' "$SIGNED_BODY"; }
wait_for 120 "customer reboot finished" cust_running || fail "reboot"
[ "$(curl -s -o /dev/null -w '%{http_code}' -b "$CJAR" "$PANEL/api/v1/servers")" = 401 ] || fail "a customer session must not reach operator endpoints"
[ "$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" "$PANEL/api/v1/customer/vps")" = 401 ] || fail "an operator session must not reach the customer API"
[ "$(pg "SELECT count(*) FROM audit_logs WHERE action = 'customer.vps_action' AND metadata->>'customer' = 'ci-customer@example.com'")" = 1 ] || fail "customer audit"
[ "$(signed DELETE "/api/v1/provisioning/services/$SVC_ID" '' ci-terminate-order-1)" = 202 ] || { cat "$SIGNED_BODY"; fail "terminate"; }
wait_for 180 "billed service terminated" svc_is terminated || fail "not terminated"
wait_for 60 "service.terminated webhook" hook_has service.terminated || fail "no service.terminated webhook"
[ "$(pg "SELECT count(*) FROM audit_logs WHERE action = 'provisioning.service_create' AND metadata->>'api_client' IS NOT NULL")" = 1 ] || fail "audit attribution"
kill "$BILL_PID" || true
login

api DELETE "/provider/nodes/$NODE_ID" >/dev/null || fail "remove node"
api PUT /settings/panel-mode '{"mode":"personal"}' >/dev/null

step "live metrics arrive (CPU needs two readings: first upload after ~30 s)"
has_metrics() { api GET "/servers/$SERVER_ID/metrics?minutes=10" | jq -e '[.samples[] | select(.cpu_percent != null and .mem_total_bytes > 0 and .load1 != null)] | length > 0'; }
wait_for 90 "metrics with CPU, memory and load" has_metrics

step "backup"
sudo "$INSTALLER" backup
sudo test -s /var/backups/osriva/latest/database.sql.gz
# Backups within the same second get their own directories (they used to overwrite each other).
BEFORE="$(sudo find /var/backups/osriva -mindepth 1 -maxdepth 1 -type d | wc -l)"
sudo "$INSTALLER" backup >/dev/null && sudo "$INSTALLER" backup >/dev/null
[ "$(sudo find /var/backups/osriva -mindepth 1 -maxdepth 1 -type d | wc -l)" -eq $((BEFORE + 2)) ] || fail "two backups did not make two directories"

step "restore a chosen backup as on a new host (different secret key; later data is gone)"
RESTORE_FROM="$(sudo readlink -f /var/backups/osriva/latest)"
AFTER_ID="$(api POST /server-groups '{"name":"made after the backup","description":"must disappear"}' | jq -r .id)"
[ -n "$AFTER_ID" ] && [ "$AFTER_ID" != null ] || fail "group after backup"
# A fresh host has its own secret key: put a different one in place.
sudo sed -i "s|^OSRIVA_SECRET_KEY=.*|OSRIVA_SECRET_KEY=$(head -c 32 /dev/urandom | base64 -w0)|" /etc/osriva/osriva.env
# A damaged or missing backup is refused before anything changes.
! sudo "$INSTALLER" restore --yes >/dev/null 2>&1 || fail 'restore without --from accepted'
BAD="$(mktemp -d)"; sudo cp "$RESTORE_FROM/osriva.env" "$BAD/"; echo broken | sudo tee "$BAD/database.sql.gz" >/dev/null
! sudo "$INSTALLER" restore --from "$BAD" --yes >/dev/null 2>&1 || fail 'damaged backup accepted'
sudo "$INSTALLER" restore --from "$RESTORE_FROM" --yes
sudo test -f /etc/osriva/osriva.env.before-restore
[ "$(sudo sed -n 's/^OSRIVA_SECRET_KEY=//p' /etc/osriva/osriva.env)" = "$(sudo sed -n 's/^OSRIVA_SECRET_KEY=//p' "$RESTORE_FROM/osriva.env")" ] || fail "secret key not restored"
login
! api GET /server-groups | jq -e --arg id "$AFTER_ID" '[.groups[] | select(.id == $id)] | length > 0' >/dev/null || fail 'group created after the backup survived'
wait_for 150 "server online after restore (agent CA decrypted with the restored key)" is_online
# Rolling back now would replace the restored data with an older state: refused.
! sudo "$INSTALLER" rollback --yes >/dev/null 2>&1 || fail 'rollback right after a restore was accepted'
is_online || fail "the refused rollback changed something"

step "backup retention, an off-host copy (a local directory here) and a daily systemd timer"
OFFSITE=/tmp/osriva-offsite
sudo rm -rf "$OFFSITE"
for i in 1 2 3; do sudo "$INSTALLER" backup --keep 2 --copy-to "$OFFSITE" >/dev/null; done
count_backups() { sudo find /var/backups/osriva -mindepth 1 -maxdepth 1 -type d | grep -cE '/[0-9]{8}T[0-9]{6}Z(-[0-9]+)?$'; }
[ "$(count_backups)" -eq 2 ] || fail "retention kept $(count_backups) backups, want 2"
LATEST="$(sudo readlink -f /var/backups/osriva/latest)"
sudo cmp "$LATEST/database.sql.gz" "$OFFSITE/$(basename "$LATEST")/database.sql.gz" || fail "off-host copy differs"
[ "$(sudo find "$OFFSITE" -mindepth 1 -maxdepth 1 -type d | wc -l)" -eq 3 ] || fail "copies are not kept at the destination"
! sudo "$INSTALLER" backup --keep 0 >/dev/null 2>&1 || fail 'accepted --keep 0'
! sudo "$INSTALLER" backup --copy-to '/tmp/x;reboot' >/dev/null 2>&1 || fail 'accepted an unsafe --copy-to'
sudo "$INSTALLER" schedule-backup --at 03:15 --keep 3 --yes
systemctl is-enabled --quiet osriva-backup.timer || fail "timer not enabled"
systemctl list-timers --all osriva-backup.timer | grep -q osriva-backup.timer || fail "timer not listed"
sudo systemctl start osriva-backup.service # oneshot: returns when the backup is done
[ "$(sudo readlink -f /var/backups/osriva/latest)" != "$LATEST" ] || fail "the timer's service made no new backup"
[ "$(count_backups)" -eq 3 ] || fail "the timer's backup did not apply --keep 3"
# Each successful run is recorded for the "Panel backup missing" alert rule.
[ "$(pg "SELECT value->>'name' FROM settings WHERE key = 'panel.backup.last_success'")" = "$(basename "$(sudo readlink -f /var/backups/osriva/latest)")" ] || fail "backup not recorded in the panel"
PB_RULE="$(api POST /alerts/rules '{"name":"ci panel backup","metric":"panel_backup","threshold":26,"duration_minutes":1,"channel_ids":[],"enabled":true}' | jq -r .id)"
[ -n "$PB_RULE" ] && [ "$PB_RULE" != null ] || fail "panel backup rule"
! api POST /alerts/rules "$(jq -nc --arg s "$SERVER_ID" '{name:"bad",server_id:$s,metric:"panel_backup",threshold:26,duration_minutes:1,channel_ids:[],enabled:true}')" >/dev/null 2>&1 || fail 'panel backup rule with a server accepted'
api DELETE "/alerts/rules/$PB_RULE" >/dev/null || fail "delete panel backup rule"
sudo "$INSTALLER" unschedule-backup
[ ! -e /etc/systemd/system/osriva-backup.timer ] && [ ! -e /etc/systemd/system/osriva-backup.service ] || fail "timer not removed"

step "upgrade (same source as a new release) keeps data"
sudo "$INSTALLER" upgrade --yes
login
wait_for 150 "server online after upgrade" is_online

step "a scheduled backup after the upgrade does not change what rollback restores; runs take turns"
PRE="$(sudo readlink -f /var/backups/osriva/pre-upgrade)"
[ -n "$PRE" ] && sudo test -f "$PRE/database.sql.gz" || fail "no pre-upgrade backup recorded"
POST_ID="$(api POST /server-groups '{"name":"made after the upgrade","description":"rollback must remove it"}' | jq -r .id)"
# Another installer run holds the lock: the backup waits for it instead of running alongside.
sudo flock /run/osriva-install.lock sleep 8 &
sleep 1
WAIT_OUT="$(sudo "$INSTALLER" backup --keep 1)"
grep -q "Another osriva-install.sh run is in progress" <<<"$WAIT_OUT" || fail "backup did not wait for the lock"
wait
[ "$(sudo readlink -f /var/backups/osriva/latest)" != "$PRE" ] || fail "latest did not move"
sudo test -f "$PRE/database.sql.gz" || fail "--keep 1 pruned the pre-upgrade backup"

step "rollback restores the previous release and the backup taken before the upgrade"
sudo "$INSTALLER" rollback --yes
sudo "$INSTALLER" verify
login
wait_for 150 "server online after rollback" is_online
! api GET /server-groups | jq -e --arg id "$POST_ID" '[.groups[] | select(.id == $id)] | length > 0' >/dev/null || fail 'rollback used a backup taken after the upgrade'

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
sudo "$INSTALLER" schedule-backup --keep 3 --yes >/dev/null # uninstall must take this away too
sudo "$INSTALLER" uninstall --purge --yes
[ ! -e /opt/osriva ] && [ ! -e /etc/osriva ]
[ ! -e /etc/systemd/system/osriva-backup.timer ] && ! systemctl list-timers --all | grep -q osriva-backup || fail "uninstall left the backup timer behind"
[ -z "$(sudo docker ps -aq --filter label=com.docker.compose.project=osriva)" ]
echo; echo "SYSTEM TEST PASSED"
