#!/bin/sh
# Installs or upgrades musdash on a Linux server with systemd.
#
#   sudo ./install.sh ./musdash-linux-amd64
#
# It creates the musdash user, installs the binary and the two services, and
# starts them. Run it again with a newer binary to upgrade. Both services
# are restarted, since they are one binary: apps keep serving while the
# control plane restarts, but the proxy's own restart leaves them
# unreachable for about a second, and for up to fifteen while it lets a
# long request that is under way (a download, a log stream) finish.
set -eu

BIN="${1:-}"
DATA=/var/lib/musdash
HERE="$(cd "$(dirname "$0")" && pwd)"

fail() { echo "install: $*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || fail "run as root (sudo $0 <binary>)"
[ -n "$BIN" ] && [ -f "$BIN" ] || fail "usage: $0 <path to the musdash binary for this machine>"
command -v systemctl >/dev/null 2>&1 || fail "systemd is required"
"$BIN" version >/dev/null 2>&1 || fail "$BIN does not run on this machine (wrong architecture?)"

if ! command -v docker >/dev/null 2>&1; then
  fail "Docker is not installed. Install it first: https://docs.docker.com/engine/install/"
fi
if ! command -v git >/dev/null 2>&1; then
  echo "install: note: git is not installed; deploying from Git repositories needs it"
fi

# A dedicated user. Membership of the docker group is what lets musdash
# manage containers; it is equivalent to root on this host.
if ! id musdash >/dev/null 2>&1; then
  useradd --system --home-dir "$DATA" --shell /usr/sbin/nologin musdash
fi
getent group docker >/dev/null 2>&1 || fail "the docker group does not exist; is Docker installed correctly?"
usermod -aG docker musdash

install -d -m 0700 -o musdash -g musdash "$DATA"
install -m 0755 "$BIN" /usr/local/bin/musdash.new
mv -f /usr/local/bin/musdash.new /usr/local/bin/musdash
install -m 0644 "$HERE/musdash-server.service" /etc/systemd/system/musdash-server.service
install -m 0644 "$HERE/musdash-proxy.service" /etc/systemd/system/musdash-proxy.service

systemctl daemon-reload
systemctl enable musdash-proxy.service musdash-server.service >/dev/null
# The proxy first: on an upgrade it restarts in about a second, and apps are
# unreachable only for that moment. It takes longer, up to the fifteen
# seconds the proxy gives requests that are under way, when one of them is
# long; new connections are refused meanwhile.
systemctl restart musdash-proxy.service
systemctl restart musdash-server.service

# Small servers run out of memory during image builds. Swap turns a crash
# into a slow build.
MEM_MB=$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo)
SWAP_MB=$(awk '/SwapTotal/ {print int($2/1024)}' /proc/meminfo)
if [ "$MEM_MB" -lt 2000 ] && [ "$SWAP_MB" -lt 500 ]; then
  cat <<NOTE

This server has ${MEM_MB} MB of memory and no swap. Building images here can
run out of memory. To add 2 GB of swap:

  fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap /swapfile
  swapon /swapfile && echo '/swapfile none swap sw 0 0' >> /etc/fstab
NOTE
fi

IP=$(hostname -I 2>/dev/null | awk '{print $1}')
cat <<DONE

musdash $(/usr/local/bin/musdash version | awk '{print $2}') is running.

  Open     http://${IP:-<server address>}:8000  and create the owner account.
  Ports    80 and 443 must be open for your apps; 8000 for the dashboard
           until you give it a domain under Settings.
  Status   systemctl status musdash-server musdash-proxy
  Logs     journalctl -u musdash-server -f
  Data     $DATA   (back up musdash.db and master.key together)
DONE
