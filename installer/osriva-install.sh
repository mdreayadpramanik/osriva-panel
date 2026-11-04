#!/usr/bin/env bash
# OSRIVA Panel installer — control plane (Docker Compose) on Linux.
#
#   sudo ./installer/osriva-install.sh plan    --domain panel.example.com
#   sudo ./installer/osriva-install.sh install --domain panel.example.com --admin-email you@example.com [--agent]
#   sudo ./installer/osriva-install.sh upgrade --source /path/to/new/checkout
#   sudo ./installer/osriva-install.sh verify | repair | backup | rollback | uninstall [--purge]
#
# Principles (see SECURITY.md): detect first, show the plan, ask before changing
# anything, never touch firewall/SSH/disk/network settings, verify after every
# change, keep a backup before every upgrade.
set -Eeuo pipefail

VERSION_FILE_NAME="RELEASE"
PREFIX="/opt/osriva"
CONF_DIR="/etc/osriva"
ENV_FILE="$CONF_DIR/osriva.env"
BACKUP_DIR="/var/backups/osriva"
PROJECT="osriva"
MIN_MEM_MB=2048
MIN_DISK_GB=10

# ---------- options ----------
CMD="${1:-}"
[ $# -gt 0 ] && shift
DOMAIN=""
ADMIN_EMAIL=""
ENABLE_AGENT=0
ASSUME_YES=0
INSTALL_DOCKER=0
PURGE=0
FROM=""
KEEP=""
COPY_TO=""
AT=""
LAST_BACKUP=""
SOURCE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

usage() {
	cat <<'EOF'
OSRIVA Panel installer

Commands:
  plan        Detect this system and print what install would do (changes nothing)
  install     Install the control plane (PostgreSQL, API, dashboard, Caddy) with Docker Compose
  upgrade     Back up, deploy --source as a new release, verify; roll back automatically on failure
  repair      Rebuild and recreate the containers of the current release, then verify
  verify      Run health checks
  backup      Dump the database and configuration to /var/backups/osriva [--keep N] [--copy-to DEST]
  schedule-backup    Run backup daily with a systemd timer (--at HH:MM, --keep N, optional --copy-to DEST)
  unschedule-backup  Remove that timer (existing backups stay)
  rollback    Restore the previous release and the database backup taken before the last upgrade
  restore     Restore a chosen backup (--from DIR) into this installation, e.g. on a new host
  uninstall   Stop and remove the containers and /opt/osriva (data kept unless --purge)

Options:
  --domain NAME          Public host name (default: localhost). Real domains get Let's Encrypt certificates.
  --admin-email EMAIL    Create the first owner account (password printed once)
  --agent                Enable the agent endpoint on port 8443
  --source DIR           OSRIVA source checkout to install from (default: this checkout)
  --install-docker       Allow installing Docker from the distribution's packages if it is missing (Ubuntu only)
  --purge                With uninstall: also delete the database, configuration and backups
  --from DIR             With restore: the backup directory (e.g. /var/backups/osriva/<timestamp>)
  --keep N               With backup: afterwards keep only the newest N backups (1-365)
  --copy-to DEST         With backup: copy the new backup with rsync to /local/dir or user@host:/dir
                         (SSH keys are yours to set up; the copy contains the secret key)
  --at HH:MM             With schedule-backup: daily time (host time zone, default 03:15)
  --yes                  Do not prompt (non-interactive)
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--domain) DOMAIN="${2:?--domain needs a value}"; shift 2 ;;
	--admin-email) ADMIN_EMAIL="${2:?--admin-email needs a value}"; shift 2 ;;
	--agent) ENABLE_AGENT=1; shift ;;
	--source) SOURCE="$(cd "${2:?--source needs a value}" && pwd)"; shift 2 ;;
	--install-docker) INSTALL_DOCKER=1; shift ;;
	--purge) PURGE=1; shift ;;
	--from) FROM="${2:?--from needs a value}"; shift 2 ;;
	--keep) KEEP="${2:?--keep needs a value}"; shift 2 ;;
	--copy-to) COPY_TO="${2:?--copy-to needs a value}"; shift 2 ;;
	--at) AT="${2:?--at needs a value}"; shift 2 ;;
	--yes | -y) ASSUME_YES=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
	esac
done

# Commands other than install act on an existing installation: take the domain
# from its configuration unless one was given explicitly.
if [ -z "$DOMAIN" ] && [ -r "$ENV_FILE" ]; then
	DOMAIN="$(sed -n 's/^OSRIVA_DOMAIN=//p' "$ENV_FILE" | tail -n 1)"
fi

# ---------- output helpers ----------
if [ -t 1 ]; then B=$'\e[1m'; G=$'\e[32m'; Y=$'\e[33m'; R=$'\e[31m'; N=$'\e[0m'; else B="" G="" Y="" R="" N=""; fi
info() { printf '%s==>%s %s\n' "$B" "$N" "$*"; }
ok() { printf '  %s✓%s %s\n' "$G" "$N" "$*"; }
warn() { printf '  %s!%s %s\n' "$Y" "$N" "$*"; }
bad() { printf '  %s✗%s %s\n' "$R" "$N" "$*"; }
die() { printf '%serror:%s %s\n' "$R" "$N" "$*" >&2; exit 1; }
trap 'case "$BASH_COMMAND" in return*) ;; *) die "command failed at line $LINENO: $BASH_COMMAND" ;; esac' ERR

confirm() {
	[ "$ASSUME_YES" -eq 1 ] && return 0
	[ -t 0 ] || die "no terminal to ask for confirmation; re-run with --yes after reading the plan"
	local reply
	read -r -p "$1 [y/N] " reply
	[[ "$reply" =~ ^[Yy]$ ]]
}

compose() {
	docker compose --project-name "$PROJECT" --env-file "$ENV_FILE" -f "$PREFIX/current/docker-compose.yml" "$@"
}

# ---------- detection ----------
OS_ID="" OS_VERSION="" OS_NAME="" ARCH="" MEM_MB=0 DISK_GB=0
HAVE_DOCKER=0 HAVE_COMPOSE=0 DOCKER_VERSION="" HAVE_SYSTEMD=0
PROBLEMS=() WARNINGS=() BUSY_PORTS=()

detect() {
	[ "$(id -u)" -eq 0 ] || die "run as root (for example with sudo)"
	[ "$(uname -s)" = Linux ] || die "this installer supports Linux only; see COMPATIBILITY_MATRIX.md"
	if [ -r /etc/os-release ]; then
		# shellcheck disable=SC1091
		. /etc/os-release
		OS_ID="${ID:-unknown}" OS_VERSION="${VERSION_ID:-}" OS_NAME="${PRETTY_NAME:-$OS_ID}"
	fi
	case "$(uname -m)" in
	x86_64) ARCH=amd64 ;;
	aarch64 | arm64) ARCH=arm64 ;;
	*) ARCH="$(uname -m)"; PROBLEMS+=("unsupported CPU architecture $ARCH (need x86_64 or arm64)") ;;
	esac
	MEM_MB=$(awk '/^MemTotal:/ {print int($2/1024)}' /proc/meminfo)
	[ "$MEM_MB" -ge "$MIN_MEM_MB" ] || WARNINGS+=("only ${MEM_MB} MB RAM; ${MIN_MEM_MB} MB recommended (image builds may fail)")
	# Measure the nearest existing parent: detection must not create anything.
	local d="$PREFIX"
	while [ ! -d "$d" ]; do d="$(dirname "$d")"; done
	DISK_GB=$(df -Pk "$d" 2>/dev/null | awk 'NR==2 {print int($4/1048576)}')
	[ "${DISK_GB:-0}" -ge "$MIN_DISK_GB" ] || WARNINGS+=("only ${DISK_GB} GB free under $PREFIX; ${MIN_DISK_GB} GB recommended")
	[ -d /run/systemd/system ] && HAVE_SYSTEMD=1
	if command -v docker >/dev/null 2>&1; then
		HAVE_DOCKER=1
		DOCKER_VERSION="$(docker version --format '{{.Server.Version}}' 2>/dev/null || echo "daemon not reachable")"
		docker compose version >/dev/null 2>&1 && HAVE_COMPOSE=1
	fi
	local ports="80 443"
	if [ "$ENABLE_AGENT" -eq 1 ]; then ports="$ports 8443"; fi
	for p in $ports; do
		if command -v ss >/dev/null 2>&1 && ss -Hltn "sport = :$p" 2>/dev/null | grep -q .; then BUSY_PORTS+=("$p"); fi
	done
	if [ -z "$DOMAIN" ]; then DOMAIN="localhost"; fi
	[[ "$DOMAIN" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$ ]] || PROBLEMS+=("invalid --domain '$DOMAIN'")
	[ -z "$ADMIN_EMAIL" ] || [[ "$ADMIN_EMAIL" =~ ^[^@[:space:]]+@[^@[:space:]]+$ ]] || PROBLEMS+=("invalid --admin-email")
	for f in docker-compose.yml go.mod infrastructure/docker/api.Dockerfile apps/dashboard/package.json; do
		[ -e "$SOURCE/$f" ] || PROBLEMS+=("--source $SOURCE is not an OSRIVA checkout (missing $f)")
	done
}

print_detection() {
	info "System"
	ok "OS: ${OS_NAME:-unknown} ($OS_ID ${OS_VERSION})"
	ok "Architecture: $ARCH"
	ok "Memory: ${MEM_MB} MB, free disk under $PREFIX: ${DISK_GB} GB"
	if [ "$HAVE_DOCKER" -eq 1 ]; then ok "Docker: $DOCKER_VERSION"; else warn "Docker: not installed"; fi
	if [ "$HAVE_COMPOSE" -eq 1 ]; then ok "Docker Compose v2: available"; elif [ "$HAVE_DOCKER" -eq 1 ]; then warn "Docker Compose v2: missing"; fi
	if [ "${#BUSY_PORTS[@]}" -gt 0 ]; then warn "ports already in use: ${BUSY_PORTS[*]}"; fi
	local w p
	for w in "${WARNINGS[@]}"; do warn "$w"; done
	for p in "${PROBLEMS[@]}"; do bad "$p"; done
}

docker_plan_line() {
	if [ "$HAVE_DOCKER" -eq 1 ] && [ "$HAVE_COMPOSE" -eq 1 ]; then return; fi
	if [ "$OS_ID" = ubuntu ] && [ "$INSTALL_DOCKER" -eq 1 ]; then
		echo "  - install Docker from Ubuntu's own packages: apt-get install docker.io docker-compose-v2"
	else
		PROBLEMS+=("Docker Engine with the Compose v2 plugin is required. Install it (https://docs.docker.com/engine/install/), or on Ubuntu re-run with --install-docker")
	fi
}

ensure_docker() {
	if [ "$HAVE_DOCKER" -eq 1 ] && [ "$HAVE_COMPOSE" -eq 1 ]; then return; fi
	info "Installing Docker from Ubuntu packages"
	DEBIAN_FRONTEND=noninteractive apt-get update -q
	DEBIAN_FRONTEND=noninteractive apt-get install -y -q docker.io docker-compose-v2
	[ "$HAVE_SYSTEMD" -eq 1 ] && systemctl enable --now docker
	docker compose version >/dev/null || die "Docker Compose v2 is still unavailable after installation"
}

# ---------- release management ----------
# ensure_plugin_dir creates the plugin directory the API mounts read-only (only
# root may write it; plugins stay disabled until OSRIVA_PLUGIN_TRUSTED_KEYS is set).
ensure_plugin_dir() {
	install -d -m 0700 "$CONF_DIR"
	install -d -m 0755 "$CONF_DIR/plugins"
}

new_release() {
	local id dest
	ensure_plugin_dir
	id="$(date -u +%Y%m%dT%H%M%SZ)"
	dest="$PREFIX/releases/$id"
	mkdir -p "$dest"
	# Copy only what the build needs; never local secrets, node_modules or build output.
	(cd "$SOURCE" && tar --exclude='node_modules' --exclude='.next' --exclude='.git' --exclude='.env*' \
		--exclude='bin' --exclude='dist' -cf - go.mod go.sum migrations pkg services agent apps package.json package-lock.json \
		infrastructure installer docker-compose.yml .dockerignore) | tar -xf - -C "$dest"
	git -C "$SOURCE" rev-parse --short HEAD >"$dest/$VERSION_FILE_NAME" 2>/dev/null || echo "$id" >"$dest/$VERSION_FILE_NAME"
	echo "$dest"
}

switch_to() { ln -sfn "$1" "$PREFIX/current.new" && mv -Tf "$PREFIX/current.new" "$PREFIX/current"; }

random_b64() { head -c "$1" /dev/urandom | base64 | tr -d '\n'; }
random_hex() { head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'; }

write_env() {
	if [ -f "$ENV_FILE" ]; then
		ok "keeping existing $ENV_FILE"
		return
	fi
	install -d -m 0700 "$CONF_DIR"
	local agent_lines=""
	if [ "$ENABLE_AGENT" -eq 1 ]; then
		agent_lines="OSRIVA_PUBLIC_URL=https://$DOMAIN
OSRIVA_AGENT_ADDR=0.0.0.0:8443
OSRIVA_AGENT_PUBLIC_URL=https://$DOMAIN:8443"
	else
		agent_lines="OSRIVA_PUBLIC_URL=
OSRIVA_AGENT_ADDR=
OSRIVA_AGENT_PUBLIC_URL="
	fi
	umask 077
	cat >"$ENV_FILE" <<EOF
# Generated by osriva-install on $(date -u +%FT%TZ). Keep this file secret and backed up:
# losing OSRIVA_SECRET_KEY makes encrypted secrets (TOTP, agent CA key) unreadable.
OSRIVA_DOMAIN=$DOMAIN
OSRIVA_SECRET_KEY=$(random_b64 32)
POSTGRES_PASSWORD=$(random_hex 24)
$agent_lines
EOF
	chmod 0600 "$ENV_FILE"
	ok "wrote $ENV_FILE (mode 0600)"
}

# wait_ready waits until the API answers internally AND the public HTTPS
# routes through Caddy work (containers need a few seconds after recreation).
wait_ready() {
	local i
	for i in $(seq 1 90); do
		if compose exec -T caddy wget -qO- http://api:8080/readyz >/dev/null 2>&1 &&
			https_get /api/v1/meta >/dev/null 2>&1 && https_get /login >/dev/null 2>&1; then
			return 0
		fi
		sleep 2
	done
	return 1
}

# ---------- checks ----------
VERIFY_FAILED=0
check() {
	local name="$1" out
	shift
	if out="$("$@" 2>&1)"; then
		ok "$name"
	else
		bad "$name${out:+: $(printf '%s' "$out" | tail -n 1 | cut -c1-200)}"
		VERIFY_FAILED=1
	fi
}

running() {
	local id
	id="$(compose ps -q "$1")"
	[ -n "$id" ] && [ "$(docker inspect -f '{{.State.Running}}' "$id")" = true ]
}

agent_enabled() { grep -q '^OSRIVA_AGENT_ADDR=.' "$ENV_FILE"; }

https_get() {
	curl -fsS --max-time 10 -k --resolve "$DOMAIN:443:127.0.0.1" "https://$DOMAIN$1"
}

do_verify() {
	VERIFY_FAILED=0
	info "Verifying https://$DOMAIN"
	[ -L "$PREFIX/current" ] || die "OSRIVA is not installed ($PREFIX/current missing)"
	[ -n "$DOMAIN" ] || die "no domain: pass --domain or check OSRIVA_DOMAIN in $ENV_FILE"
	local svc
	for svc in postgres api dashboard caddy; do
		check "container $svc running" running "$svc"
	done
	check "database reachable (api /readyz)" compose exec -T caddy wget -qO- http://api:8080/readyz
	check "dashboard served over HTTPS (/login)" https_get /login
	check "API served over HTTPS (/api/v1/meta)" https_get /api/v1/meta
	check "internal probes not public (/readyz is 404 via Caddy)" sh -c "! curl -fsS --max-time 10 -k --resolve '$DOMAIN:443:127.0.0.1' 'https://$DOMAIN/readyz'"
	if agent_enabled; then
		check "agent CA published (/api/v1/agent/ca)" https_get /api/v1/agent/ca
		check "agent builds available (/api/v1/agent/downloads)" sh -c "curl -fsS --max-time 10 -k --resolve '$DOMAIN:443:127.0.0.1' 'https://$DOMAIN/api/v1/agent/downloads' | grep -q linux"
		check "agent port 8443 requires a client certificate" sh -c "! curl -fsS --max-time 10 -k https://127.0.0.1:8443/agent/v1/report -d '{}'"
	fi
	check "PostgreSQL not published on the host" sh -c "! (ss -Hltn 'sport = :5432' | grep -q .)"
	if [ "$VERIFY_FAILED" -ne 0 ]; then
		warn "some checks failed; see: docker compose --project-name $PROJECT logs"
		return 1
	fi
	ok "all checks passed"
}

do_backup() {
	local ts dir
	ts="$(date -u +%Y%m%dT%H%M%SZ)"
	install -d -m 0700 "$BACKUP_DIR"
	# Never reuse a directory: two backups within one second (e.g. the safety
	# backup a restore takes) must not overwrite each other.
	dir="$BACKUP_DIR/$ts"
	local n=1
	until mkdir -m 0700 "$dir" 2>/dev/null; do
		n=$((n + 1))
		[ "$n" -le 100 ] || die "cannot create a new backup directory in $BACKUP_DIR"
		dir="$BACKUP_DIR/$ts-$n"
	done
	info "Backing up to $dir"
	compose exec -T postgres pg_dump -U osriva -d osriva --no-owner | gzip >"$dir/database.sql.gz"
	gzip -t "$dir/database.sql.gz"
	[ "$(gzip -dc "$dir/database.sql.gz" | grep -c 'PostgreSQL database dump complete')" -ge 1 ] || die "database dump is incomplete"
	cp -p "$ENV_FILE" "$dir/osriva.env"
	readlink -f "$PREFIX/current" >"$dir/release"
	ln -sfn "$dir" "$BACKUP_DIR/latest"
	LAST_BACKUP="$dir"
	ok "database dump verified, configuration copied"
}

# Backup directory names written by do_backup; nothing else in $BACKUP_DIR is ever pruned.
BACKUP_NAME_RE='^[0-9]{8}T[0-9]{6}Z(-[0-9]+)?$'

check_backup_options() {
	if [ -n "$KEEP" ]; then
		[[ "$KEEP" =~ ^[0-9]+$ ]] && [ "$KEEP" -ge 1 ] && [ "$KEEP" -le 365 ] || die "--keep must be a number from 1 to 365"
	fi
	if [ -n "$COPY_TO" ]; then
		# Restrictive on purpose: the value ends up in a systemd unit and an rsync argument.
		[[ "$COPY_TO" =~ ^([A-Za-z0-9._-]+@[A-Za-z0-9.-]+:)?/[A-Za-z0-9._/-]+$ ]] ||
			die "--copy-to must be /absolute/dir or user@host:/absolute/dir (letters, digits, . _ - / only)"
		command -v rsync >/dev/null || die "--copy-to needs rsync; install it yourself (OSRIVA installs nothing for this)"
	fi
}

# prune_backups keeps the newest $KEEP backup directories and never removes the
# one `latest` points to.
prune_backups() {
	local latest pre d removed=0
	local -a all
	latest="$(readlink -f "$BACKUP_DIR/latest" 2>/dev/null || true)"
	pre="$(readlink -f "$BACKUP_DIR/pre-upgrade" 2>/dev/null || true)"
	mapfile -t all < <(find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | grep -E "$BACKUP_NAME_RE" | sort -r)
	for d in "${all[@]:$KEEP}"; do
		# never the newest pointer, nor the dump a rollback needs
		[ "$BACKUP_DIR/$d" = "$latest" ] || [ "$BACKUP_DIR/$d" = "$pre" ] && continue
		rm -rf -- "${BACKUP_DIR:?}/$d"
		removed=$((removed + 1))
	done
	ok "kept the newest $KEEP backups, removed $removed"
}

copy_backup() {
	local name
	name="$(basename "$LAST_BACKUP")"
	info "Copying $name to $COPY_TO"
	if [[ "$COPY_TO" == /* ]]; then
		install -d -m 0700 "$COPY_TO"
		rsync -a "$LAST_BACKUP" "$COPY_TO/"
		cmp -s "$LAST_BACKUP/database.sql.gz" "$COPY_TO/$name/database.sql.gz" || die "the copy in $COPY_TO differs from the backup"
	else
		# BatchMode: fail instead of waiting for a password in a timer.
		rsync -a -e "ssh -o BatchMode=yes" "$LAST_BACKUP" "$COPY_TO/"
	fi
	ok "copied (it contains the secret key: protect $COPY_TO accordingly)"
}

cmd_backup() {
	[ -L "$PREFIX/current" ] || die "not installed"
	check_backup_options
	do_backup
	[ -z "$COPY_TO" ] || copy_backup
	[ -z "$KEEP" ] || prune_backups
	record_backup_success
}

# record_backup_success tells the panel (setting panel.backup.last_success) so
# that a "Panel backup missing" alert rule can notice when backups stop. Only a
# fully successful run (dump verified, copy done) is recorded.
record_backup_success() {
	local name
	name="$(basename "$LAST_BACKUP")"
	[[ "$name" =~ $BACKUP_NAME_RE ]] || die "unexpected backup name $name"
	if compose exec -T postgres psql -q -U osriva -d osriva -v ON_ERROR_STOP=1 -c \
		"INSERT INTO settings (key, value, updated_at) VALUES ('panel.backup.last_success',
		 jsonb_build_object('at_unix', extract(epoch FROM now())::bigint, 'name', '$name'), now())
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()" >/dev/null; then
		ok "recorded in the panel (alert metric: Panel backup missing)"
	else
		warn "could not record the backup in the panel; a Panel backup missing alert may fire"
	fi
}

UNIT_DIR="/etc/systemd/system"

cmd_schedule_backup() {
	[ -L "$PREFIX/current" ] || die "not installed"
	command -v systemctl >/dev/null && [ -d /run/systemd/system ] || die "schedule-backup needs systemd"
	AT="${AT:-03:15}"
	[[ "$AT" =~ ^([01][0-9]|2[0-3]):[0-5][0-9]$ ]] || die "--at must be HH:MM (24 hours)"
	KEEP="${KEEP:-7}"
	check_backup_options
	local args="backup --keep $KEEP"
	[ -z "$COPY_TO" ] || args="$args --copy-to $COPY_TO"
	info "Backup schedule plan"
	echo "  - write $UNIT_DIR/osriva-backup.service and osriva-backup.timer"
	echo "  - every day at $AT (host time zone; missed runs catch up after boot): $PREFIX/current/installer/osriva-install.sh $args"
	[ -z "$COPY_TO" ] && warn "no --copy-to: the backups stay on this disk and do not survive its failure"
	confirm "Create and start the timer?" || die "aborted"
	cat >"$UNIT_DIR/osriva-backup.service" <<EOF
# Written by osriva-install.sh schedule-backup. Remove with unschedule-backup.
[Unit]
Description=OSRIVA Panel backup
After=docker.service
Requires=docker.service

[Service]
Type=oneshot
ExecStart=/bin/bash $PREFIX/current/installer/osriva-install.sh $args
EOF
	cat >"$UNIT_DIR/osriva-backup.timer" <<EOF
# Written by osriva-install.sh schedule-backup. Remove with unschedule-backup.
[Unit]
Description=Daily OSRIVA Panel backup

[Timer]
OnCalendar=*-*-* $AT:00
Persistent=true

[Install]
WantedBy=timers.target
EOF
	systemctl daemon-reload
	systemctl enable --now osriva-backup.timer >/dev/null
	ok "scheduled; check with: systemctl list-timers osriva-backup.timer; journalctl -u osriva-backup.service"
}

cmd_unschedule_backup() {
	[ -f "$UNIT_DIR/osriva-backup.timer" ] || { ok "no backup schedule"; return 0; }
	systemctl disable --now osriva-backup.timer >/dev/null 2>&1 || true
	rm -f "$UNIT_DIR/osriva-backup.timer" "$UNIT_DIR/osriva-backup.service"
	systemctl daemon-reload
	ok "backup schedule removed (existing backups are kept)"
}

restore_db() {
	local dump="$1"
	info "Restoring database from $dump"
	compose stop api dashboard >/dev/null
	# Drop everything first so tables created by a newer release's migrations do not survive.
	compose exec -T postgres psql -q -U osriva -d osriva -v ON_ERROR_STOP=1 \
		-c 'DROP SCHEMA public CASCADE' -c 'CREATE SCHEMA public' >/dev/null
	gzip -dc "$dump" | compose exec -T postgres psql -q -U osriva -d osriva -v ON_ERROR_STOP=1 >/dev/null
	ok "database restored"
}

print_access() {
	info "OSRIVA Panel is running"
	echo "  URL:    https://$DOMAIN"
	[ "$DOMAIN" = localhost ] && echo "          (localhost uses Caddy's internal CA; your browser will warn until you trust it)"
	echo "  Config: $ENV_FILE   Release: $(cat "$PREFIX/current/$VERSION_FILE_NAME")"
	local ports="80/tcp and 443/tcp (panel)"
	if agent_enabled; then ports="$ports, 8443/tcp (agents)"; fi
	echo "  Open these ports in your firewall yourself (OSRIVA never changes firewall rules):"
	echo "    $ports"
}

# ---------- commands ----------
cmd_plan() {
	detect
	print_detection
	info "Plan"
	docker_plan_line
	echo "  - copy the OSRIVA source from $SOURCE to $PREFIX/releases/<timestamp> and link $PREFIX/current"
	echo "  - create $CONF_DIR/plugins (empty; plugins stay off until OSRIVA_PLUGIN_TRUSTED_KEYS is set)"
	[ -f "$ENV_FILE" ] && echo "  - keep existing $ENV_FILE" || echo "  - create $ENV_FILE (mode 0600) with a new secret key and database password"
	echo "  - build and start containers with Docker Compose (project '$PROJECT'): postgres, migrate, api, dashboard, caddy"
	echo "  - publish ports 80, 443$([ "$ENABLE_AGENT" -eq 1 ] && echo ", 8443 (agent endpoint)") on this host; PostgreSQL stays internal"
	[ -n "$ADMIN_EMAIL" ] && echo "  - create the owner account $ADMIN_EMAIL (generated password printed once)"
	echo "  - run health checks"
	echo "  Not changed: firewall, SSH, disks, network configuration, other services."
	if [ "${#BUSY_PORTS[@]}" -gt 0 ]; then PROBLEMS+=("ports ${BUSY_PORTS[*]} are in use by other services"); fi
	if [ "${#PROBLEMS[@]}" -gt 0 ]; then
		for p in "${PROBLEMS[@]}"; do bad "$p"; done
		return 1
	fi
}

cmd_install() {
	[ ! -L "$PREFIX/current" ] || die "already installed (use upgrade, repair or uninstall)"
	cmd_plan || die "cannot install; fix the problems above"
	confirm "Proceed with the installation?" || die "aborted, nothing was changed"
	ensure_docker
	write_env
	local rel
	rel="$(new_release)"
	switch_to "$rel"
	ok "release $(cat "$rel/$VERSION_FILE_NAME") at $rel"
	info "Building and starting containers (first build takes several minutes)"
	compose up -d --build
	info "Waiting for the API"
	wait_ready || die "API did not become ready; inspect: docker compose --project-name $PROJECT logs api migrate"
	if [ -n "$ADMIN_EMAIL" ]; then
		info "Creating the owner account"
		compose run --rm -T --no-deps api admin create --email "$ADMIN_EMAIL" --name "Owner" --role owner
	fi
	do_verify
	print_access
}

cmd_upgrade() {
	[ -L "$PREFIX/current" ] || die "not installed"
	detect
	[ "${#PROBLEMS[@]}" -eq 0 ] || { print_detection; die "cannot upgrade"; }
	local cur
	cur="$(readlink -f "$PREFIX/current")"
	info "Upgrade plan"
	echo "  - back up the database and configuration"
	echo "  - deploy $SOURCE as a new release (current: $(cat "$cur/$VERSION_FILE_NAME"))"
	echo "  - rebuild and restart containers, run migrations, verify"
	echo "  - on failure: restore the previous release and the database backup"
	confirm "Proceed with the upgrade?" || die "aborted, nothing was changed"
	do_backup
	# Rollback must restore exactly this dump: `latest` moves with every later
	# (e.g. scheduled) backup, whose schema belongs to the new release.
	ln -sfn "$LAST_BACKUP" "$BACKUP_DIR/pre-upgrade"
	local rel
	rel="$(new_release)"
	ln -sfn "$cur" "$PREFIX/previous"
	switch_to "$rel"
	info "Starting release $(cat "$rel/$VERSION_FILE_NAME")"
	if compose up -d --build && wait_ready && do_verify; then
		ok "upgrade complete"
		print_access
		return
	fi
	warn "upgrade failed; rolling back"
	do_rollback_inner
	die "upgrade failed and was rolled back to $(cat "$PREFIX/current/$VERSION_FILE_NAME")"
}

# rollback_backup is the backup taken right before the last upgrade
# (installations upgraded before pre-upgrade existed fall back to latest).
rollback_backup() {
	if [ -L "$BACKUP_DIR/pre-upgrade" ]; then readlink -f "$BACKUP_DIR/pre-upgrade"; else readlink -f "$BACKUP_DIR/latest" 2>/dev/null || true; fi
}

# unknown_migrations prints the migrations a dump has applied that a release
# does not contain (empty: the release understands the dump's schema).
unknown_migrations() { # DUMP RELEASE_DIR
	gzip -dc "$1" | awk '/^COPY public.schema_migrations /{c=1; next} c && /^\\\.$/{c=0} c {print $1}' |
		while read -r v; do [ -f "$2/migrations/$v.sql" ] || echo "$v"; done
}

do_rollback_inner() {
	[ -L "$PREFIX/previous" ] || die "no previous release to roll back to"
	local bdir prev newer
	bdir="$(rollback_backup)"
	[ -n "$bdir" ] && [ -f "$bdir/database.sql.gz" ] || die "no database backup from before the upgrade found in $BACKUP_DIR"
	prev="$(readlink -f "$PREFIX/previous")"
	newer="$(unknown_migrations "$bdir/database.sql.gz" "$prev")"
	[ -z "$newer" ] || die "$bdir has migrations the previous release does not know ($(echo $newer)); refusing to roll back with it"
	restore_db "$bdir/database.sql.gz"
	switch_to "$prev"
	compose up -d --build
	wait_ready || die "previous release did not become ready"
	do_verify || true
}

cmd_rollback() {
	[ -L "$PREFIX/current" ] || die "not installed"
	info "Rollback plan"
	echo "  - restore the database from $(rollback_backup), taken before the last upgrade (data written since is LOST)"
	echo "  - switch to $(readlink -f "$PREFIX/previous" 2>/dev/null || echo '<none>')"
	confirm "Proceed with the rollback?" || die "aborted"
	do_rollback_inner
	ok "rolled back to $(cat "$PREFIX/current/$VERSION_FILE_NAME")"
}

# cmd_restore restores a backup chosen by the operator, e.g. one copied from a
# failed host onto a fresh installation. The database is replaced and the
# backup's secret key is taken over (without it, stored secrets and the agent CA
# cannot be decrypted); this host's database password and other settings stay.
cmd_restore() {
	[ -L "$PREFIX/current" ] || die "not installed: install first, then restore into the new installation"
	[ -n "$FROM" ] || die "restore needs --from DIR (a directory written by 'backup')"
	local dir dump key cur_key newer
	dir="$(readlink -f "$FROM")"
	dump="$dir/database.sql.gz"
	[ -f "$dump" ] && [ -f "$dir/osriva.env" ] || die "$dir is not an OSRIVA backup (database.sql.gz and osriva.env expected)"
	gzip -t "$dump" || die "the database dump in $dir is damaged"
	[ "$(gzip -dc "$dump" | grep -c 'PostgreSQL database dump complete')" -ge 1 ] || die "the database dump in $dir is incomplete"
	key="$(sed -n 's/^OSRIVA_SECRET_KEY=//p' "$dir/osriva.env" | tail -n 1)"
	[ -n "$key" ] || die "the backup has no OSRIVA_SECRET_KEY, so its encrypted data could not be read"
	cur_key="$(sed -n 's/^OSRIVA_SECRET_KEY=//p' "$ENV_FILE" | tail -n 1)"
	# A dump from a newer release has migrations this release does not know; it
	# would start against a schema it does not understand.
	newer="$(unknown_migrations "$dump" "$(readlink -f "$PREFIX/current")")"
	[ -z "$newer" ] || die "the backup comes from a newer release (unknown migrations: $(echo $newer)); upgrade this installation first"
	info "Restore plan"
	echo "  - back up the current state first (to $BACKUP_DIR/<timestamp>)"
	echo "  - ${R}replace the database${N} with $dump (anything written here since is LOST)"
	if [ "$key" != "$cur_key" ]; then
		echo "  - take OSRIVA_SECRET_KEY from the backup (the current file is kept as $ENV_FILE.before-restore)"
	else
		echo "  - the secret key is already the backup's; $ENV_FILE stays as it is"
	fi
	echo "  - keep this host's database password, domain and agent settings; apply pending migrations; verify"
	confirm "Proceed with the restore?" || die "aborted"
	do_backup
	if [ "$key" != "$cur_key" ]; then
		cp -p "$ENV_FILE" "$ENV_FILE.before-restore"
		local tmp
		tmp="$(mktemp "$CONF_DIR/.osriva.env.XXXXXX")"
		awk -v k="$key" '/^OSRIVA_SECRET_KEY=/{print "OSRIVA_SECRET_KEY=" k; next} {print}' "$ENV_FILE" >"$tmp"
		chmod 0600 "$tmp"
		mv "$tmp" "$ENV_FILE"
		ok "secret key taken from the backup"
	fi
	restore_db "$dump"
	compose up -d --force-recreate
	wait_ready || die "the API did not become ready after the restore (previous state: $BACKUP_DIR/latest)"
	do_verify
	ok "restored from $dir"
}

cmd_repair() {
	[ -L "$PREFIX/current" ] || die "not installed"
	ensure_plugin_dir
	confirm "Rebuild and recreate all containers of the current release?" || die "aborted"
	compose up -d --build --force-recreate
	wait_ready || die "API did not become ready"
	do_verify
}

cmd_uninstall() {
	[ -L "$PREFIX/current" ] || [ -d "$PREFIX" ] || die "not installed"
	info "Uninstall plan"
	echo "  - stop and remove the OSRIVA containers and $PREFIX"
	if [ "$PURGE" -eq 1 ]; then
		echo "  - ${R}PERMANENTLY DELETE${N} the database volume, $CONF_DIR (secret key) and $BACKUP_DIR"
	else
		echo "  - keep the database volume, $CONF_DIR and $BACKUP_DIR (use --purge to delete them)"
	fi
	confirm "Proceed?" || die "aborted"
	if [ "$PURGE" -eq 1 ] && [ "$ASSUME_YES" -ne 1 ]; then
		local phrase
		read -r -p "Type 'delete everything' to confirm permanent deletion: " phrase
		[ "$phrase" = "delete everything" ] || die "aborted"
	fi
	if [ -L "$PREFIX/current" ]; then
		if [ "$PURGE" -eq 1 ]; then compose down --volumes --remove-orphans; else compose down --remove-orphans; fi
	fi
	rm -rf "$PREFIX"
	if [ "$PURGE" -eq 1 ]; then rm -rf "$CONF_DIR" "$BACKUP_DIR"; fi
	ok "uninstalled"
}

# One changing run at a time: a scheduled backup must not dump the database
# while an upgrade, rollback or restore is replacing it (and the other way round).
LOCK_FILE="/run/osriva-install.lock"
take_lock() {
	command -v flock >/dev/null || { warn "flock not found: concurrent runs are not prevented"; return 0; }
	exec 9>"$LOCK_FILE"
	if ! flock -n 9; then
		info "Another osriva-install.sh run is in progress; waiting up to 15 minutes"
		flock -w 900 9 || die "another osriva-install.sh run still holds $LOCK_FILE"
	fi
}
case "$CMD" in
install | upgrade | repair | backup | schedule-backup | unschedule-backup | rollback | restore | uninstall) take_lock ;;
esac

case "$CMD" in
plan) cmd_plan ;;
install) cmd_install ;;
upgrade) cmd_upgrade ;;
repair) cmd_repair ;;
verify) do_verify ;;
backup) cmd_backup ;;
schedule-backup) cmd_schedule_backup ;;
unschedule-backup) cmd_unschedule_backup ;;
rollback) cmd_rollback ;;
restore) cmd_restore ;;
uninstall) cmd_uninstall ;;
-h | --help | help | "") usage ;;
*) echo "unknown command: $CMD" >&2; usage >&2; exit 2 ;;
esac
