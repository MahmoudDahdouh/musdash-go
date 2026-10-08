#!/bin/sh
# Installs or upgrades musdash on a Linux server with systemd.
#
#   curl -fsSL https://github.com/MahmoudDahdouh/musdash-go/releases/latest/download/install.sh | sudo sh
#   sudo ./install.sh ./musdash-linux-amd64      a binary that is already here
#
# It installs Docker and git where they are missing, downloads the binary
# of this server's architecture and checks it against the release's
# checksums, creates the musdash user, installs the two services and starts
# them. Run again, it upgrades. Both services are one binary, so an upgrade
# restarts both: apps keep serving while the control plane restarts, but
# the proxy's own restart leaves them unreachable for about a second, and
# for up to fifteen while it lets a long request that is under way (a
# download, a log stream) finish. When nothing would change, nothing is
# restarted.
#
# Everything a command prints goes to /var/log/musdash-install.log; the
# screen is the list of steps.
#
#   MUSDASH_VERSION=v1.2.3    install that release, not the latest
#   MUSDASH_RELEASE_URL=...   take the release's files from this address
#   MUSDASH_ADDRESS=...       the address to show for the dashboard
#   NO_COLOR=1                no colour
set -eu

REPO=MahmoudDahdouh/musdash-go
# The release this copy installs. It is empty in the repository, where the
# latest release is taken; "make release" writes the tag here, so that a
# release's installer installs that release.
RELEASE=""
DATA=/var/lib/musdash
LOG=/var/log/musdash-install.log
UNITS=/etc/systemd/system
BIN=/usr/local/bin/musdash
NEWBIN=/usr/local/bin/musdash.new
PORT=8000
STEPS=7
# The lines of the checklist: three above the rows and two under them.
FRAME_LINES=12

VERSION=${MUSDASH_VERSION:-$RELEASE}
BASE=${MUSDASH_RELEASE_URL:-}
E=$(printf '\033')

FANCY=""; COLOR=""; UTF8=1; CLR=""
TMP=""; PAINTER=""; LOCAL_BIN=""; MADE_NEW=""
ARCH=""; OLD=""; NEW=""; CHANGED=""; NOTE_GIT=""
DETAIL=""; SKIP=""; LOGSTART=0
C_0=""; C_W=""; C_G=""; C_R=""; C_B=""; C_D=""; C_U=""; RAMP=""

# --- What is asked of a value ----------------------------------------------

arch_of() {
  case "$1" in
    x86_64 | amd64) echo amd64 ;;
    aarch64 | arm64) echo arm64 ;;
    *) return 1 ;;
  esac
}

# A version becomes part of a URL, so it is "v" and three numbers and
# nothing else.
valid_version() {
  expr "x$1" : 'xv[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*$' >/dev/null
}

release_url() {
  if [ -n "$BASE" ]; then
    echo "$BASE/$1"
  elif [ -n "$VERSION" ]; then
    echo "https://github.com/$REPO/releases/download/$VERSION/$1"
  else
    echo "https://github.com/$REPO/releases/latest/download/$1"
  fi
}

# The hash of the line of a checksums file whose name is exactly the file:
# a longer name that ends the same way is another file.
checksum_for() {
  awk -v f="$1" '$2 == f || $2 == "*" f { print $1; exit }' "$2"
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{ print $1 }'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{ print $1 }'
  else
    return 1
  fi
}

valid_ipv4() {
  case "$1" in '' | *[!0-9.]* | *..* | .* | *.) return 1 ;; esac
  v4_ifs=$IFS
  IFS=.
  # shellcheck disable=SC2086
  set -- $1
  IFS=$v4_ifs
  [ $# -eq 4 ] || return 1
  for v4_o in "$@"; do
    case "$v4_o" in '' | ????*) return 1 ;; esac
    [ "$v4_o" -le 255 ] || return 1
  done
  return 0
}

# Whether an address is of no use to a browser somewhere else. It is asked
# only of what valid_ipv4 let through.
is_private() {
  pr_ifs=$IFS
  IFS=.
  # shellcheck disable=SC2086
  set -- $1
  IFS=$pr_ifs
  case "$1" in
    10 | 127) return 0 ;;
    172) [ "$2" -ge 16 ] && [ "$2" -le 31 ] ;;
    192) [ "$2" -eq 168 ] ;;
    100) [ "$2" -ge 64 ] && [ "$2" -le 127 ] ;;
    169) [ "$2" -eq 254 ] ;;
    *) return 1 ;;
  esac
}

# What a server says of itself (its system's name, a version) is data: it
# is drawn as printable ASCII of a known length, so that it can neither
# move the cursor nor push a row out of its columns.
clean() {
  printf '%s' "$1" | LC_ALL=C tr -cd '\040-\176' | cut -c "1-$2"
}

# Whether blocks, the spinner and the box can be drawn. A server's locale
# is often C while the window the person looks at draws UTF-8, so only a
# locale that names another character set says no, and the Linux console,
# whose font has no braille.
utf8_ok() {
  if [ "${TERM:-}" = linux ]; then return 1; fi
  u8=${LC_ALL:-${LC_CTYPE:-${LANG:-}}}
  case "$u8" in
    *.[Uu][Tt][Ff]-8* | *.[Uu][Tt][Ff]8*) return 0 ;;
    *.*) return 1 ;;
  esac
  return 0
}

# --- The two services ------------------------------------------------------

unit_server() {
  cat <<'EOF'
[Unit]
Description=musdash control plane (UI, webhooks, jobs)
Documentation=https://github.com/MahmoudDahdouh/musdash-go
After=network-online.target docker.service
Wants=network-online.target

[Service]
User=musdash
Group=musdash
# Access to the Docker socket. This is root-equivalent on the host, which is
# why musdash runs as its own user and never passes raw input to a shell.
SupplementaryGroups=docker
Environment=MUSDASH_DATA=/var/lib/musdash
Environment=MUSDASH_LISTEN=:8000
ExecStart=/usr/local/bin/musdash server
Restart=always
RestartSec=2
# Running deployments get this long to finish before shutdown.
TimeoutStopSec=40

StateDirectory=musdash
StateDirectoryMode=0700
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectKernelTunables=true
ProtectControlGroups=true

[Install]
WantedBy=multi-user.target
EOF
}

unit_proxy() {
  cat <<'EOF'
[Unit]
Description=musdash edge proxy (ports 80 and 443)
Documentation=https://github.com/MahmoudDahdouh/musdash-go
After=network-online.target
Wants=network-online.target

[Service]
# The same user as the control plane, which signals this process to reload
# its routes.
User=musdash
Group=musdash
Environment=MUSDASH_DATA=/var/lib/musdash
ExecStart=/usr/local/bin/musdash proxy
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=1

# Bind ports 80 and 443 without running as root.
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/musdash
PrivateTmp=true
ProtectHome=true
ProtectKernelTunables=true
ProtectControlGroups=true

[Install]
WantedBy=multi-user.target
EOF
}

# --- Drawing ---------------------------------------------------------------

# A row's name, padded to its column, and its share of the bar. The bar
# moves when a step ends, by that step's share, and never by the clock:
# how long Docker's installation will take is not known.
row() {
  case "$1" in
    1) ROW="Checking this server       " WEIGHT=5 ;;
    2) ROW="Installing Docker and git  " WEIGHT=40 ;;
    3) ROW="Downloading musdash        " WEIGHT=25 ;;
    4) ROW="Creating the musdash user  " WEIGHT=5 ;;
    5) ROW="Installing the services    " WEIGHT=5 ;;
    6) ROW="Starting musdash           " WEIGHT=10 ;;
    7) ROW="Waiting for the dashboard  " WEIGHT=10 ;;
  esac
}

name_of() {
  row "$1"
  echo "$ROW" | sed 's/ *$//'
}

rep() {
  rp=""
  rp_n=$2
  while [ "$rp_n" -gt 0 ]; do
    rp="$rp$1"
    rp_n=$((rp_n - 1))
  done
  printf '%s' "$rp"
}

# look decides how the screen is drawn. The checklist redraws twelve lines
# at once, which a short window would scroll into a mess, and what is not a
# terminal (a file, a pipe, cloud-init) must get no escape sequence at all:
# both get one plain line when a step starts and one when it ends.
look() {
  FANCY=""
  COLOR=""
  UTF8=""
  CLR=""
  if utf8_ok; then UTF8=1; fi
  if [ -t 1 ] && [ "${TERM:-dumb}" != dumb ]; then
    lk=$({ stty size </dev/tty; } 2>/dev/null) || lk=""
    lk_rows=${lk% *}
    lk_cols=${lk#* }
    case "$lk_rows$lk_cols" in '' | *[!0-9]*) lk_rows=0 lk_cols=0 ;; esac
    if [ "$lk_rows" -ge 20 ] && [ "$lk_cols" -ge 66 ]; then FANCY=1; fi
  fi
  if [ -n "$FANCY" ]; then CLR="$E[2K"; fi
  if [ -n "$FANCY" ] && [ -z "${NO_COLOR:-}" ]; then COLOR=1; fi
  glyphs
  colors
}

glyphs() {
  if [ -n "$UTF8" ]; then
    G_OK="✓" G_FAIL="✗" G_WAIT="○" G_SKIP="–" G_BAR="━" G_REST="━"
    G_SPIN="⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏"
    G_TL="┏" G_TR="┓" G_BL="┗" G_BR="┛" G_H="━" G_V="┃" G_PTR="❯"
  else
    G_OK="[ok]" G_FAIL="[!!]" G_WAIT="[  ]" G_SKIP="[--]" G_BAR="#" G_REST="-"
    G_SPIN="[..] [.o] [oo] [o.]"
    G_TL="+" G_TR="+" G_BL="+" G_BR="+" G_H="-" G_V="|" G_PTR=">"
  fi
}

colors() {
  C_0="" C_W="" C_G="" C_R="" C_B="" C_D="" C_U="" RAMP=""
  if [ -z "$COLOR" ]; then return 0; fi
  C_0="$E[0m" C_W="$E[1m" C_G="$E[32m" C_R="$E[31m" C_U="$E[1;4m"
  cl_256=""
  case "${TERM:-}" in *256color* | *kitty* | *alacritty* | *ghostty*) cl_256=1 ;; esac
  if [ -n "${COLORTERM:-}" ]; then cl_256=1; fi
  if [ -n "$cl_256" ]; then
    C_B="$E[38;5;33m" C_D="$E[38;5;244m"
    RAMP="38;5;238 38;5;24 38;5;25 38;5;26 38;5;27 38;5;33"
  else
    C_B="$E[34m" C_D="$E[2m"
    RAMP="2 2;34 34 1;34 1;34 34"
  fi
}

# The steps run in this shell and the drawing in another process, so what
# the drawing knows of a step is a small file: its state, its detail and
# the second it started. It is read line by line, never sourced: a detail
# is what a server said of itself.
set_state() {
  printf '%s\n%s\n%s\n' "$2" "$(clean "${3:-}" 34)" "$(date +%s)" >"$TMP/s.$1.new"
  mv -f "$TMP/s.$1.new" "$TMP/s.$1"
}

read_state() {
  ST=wait DT="" T0=0
  if [ -f "$TMP/s.$1" ]; then
    {
      IFS= read -r ST
      IFS= read -r DT
      IFS= read -r T0
    } <"$TMP/s.$1" || true
  fi
  case "$T0" in '' | *[!0-9]*) T0=0 ;; esac
}

# frame draws the checklist. Every frame after the first starts by going
# back up to where the list begins.
frame() {
  fr_now=$(date +%s)
  fr_cur=0 fr_pct=0 fr_failed=0 fr_rows="" fr_i=1
  # shellcheck disable=SC2086
  set -- "$1" "$2" $G_SPIN
  fr_tick=$1
  fr_first=$2
  shift 2
  shift $((fr_tick % $#))
  fr_spin=$1
  while [ "$fr_i" -le "$STEPS" ]; do
    read_state "$fr_i"
    row "$fr_i"
    case "$ST" in
      ok)
        fr_pct=$((fr_pct + WEIGHT))
        fr_line="$C_G$G_OK$C_0  $ROW $C_D$DT$C_0"
        ;;
      skip)
        fr_pct=$((fr_pct + WEIGHT))
        fr_line="$C_D$G_SKIP  $ROW $DT$C_0"
        ;;
      run)
        fr_cur=$fr_i
        fr_line="$C_B$fr_spin$C_0  $C_W$ROW$C_0 $C_B$((fr_now - T0))s$C_0"
        ;;
      fail)
        fr_failed=$fr_i
        fr_line="$C_R$G_FAIL$C_0  $ROW $C_R$DT$C_0"
        ;;
      *)
        fr_line="$C_D$G_WAIT  $ROW$C_0"
        ;;
    esac
    fr_rows="$fr_rows$CLR  $fr_line
"
    fr_i=$((fr_i + 1))
  done
  if [ "$fr_failed" -gt 0 ]; then
    fr_head="${C_R}stopped at step $fr_failed of $STEPS$C_0"
  elif [ "$fr_cur" -gt 0 ]; then
    fr_head="${C_D}step $fr_cur of $STEPS$C_0"
  elif [ "$fr_pct" -ge 100 ]; then
    fr_head="${C_G}done$C_0"
  else
    fr_head=""
  fi
  fr_fill=$((fr_pct * 44 / 100))
  fr_up=""
  if [ "$fr_first" != 1 ]; then fr_up="$E[${FRAME_LINES}A"; fi
  printf '%s%s\n%s  %smusdash installer%s   %s\n%s\n%s%s\n%s  %s%s%s%s%s  %s%%\n' \
    "$fr_up" "$CLR" \
    "$CLR" "$C_W" "$C_0" "$fr_head" \
    "$CLR" \
    "$fr_rows" "$CLR" \
    "$CLR" "$C_B" "$(rep "$G_BAR" "$fr_fill")" "$C_D" "$(rep "$G_REST" $((44 - fr_fill)))" "$C_0" "$fr_pct"
}

# painter draws ten frames a second until it is told to stop. It ends only
# between two frames and draws the last one itself: a loop killed in the
# middle of a frame would leave the cursor somewhere in the list, and what
# is printed next would be drawn over it.
painter() {
  set +e
  pt_stop=""
  trap 'pt_stop=1' TERM
  trap '' INT
  pt_tick=0
  frame 0 1
  : >"$TMP/painting"
  while [ -z "$pt_stop" ]; do
    sleep 0.1 2>/dev/null || sleep 1
    pt_tick=$((pt_tick + 1))
    frame "$pt_tick" 0
  done
  frame "$pt_tick" 0
}

start_painter() {
  if [ -z "$FANCY" ]; then return 0; fi
  printf '%s' "$E[?25l"
  painter &
  PAINTER=$!
  # Until the loop has its trap, stopping it would kill it mid-frame.
  sp_n=0
  while [ ! -f "$TMP/painting" ] && [ "$sp_n" -lt 50 ]; do
    sleep 0.1 2>/dev/null || sleep 1
    sp_n=$((sp_n + 1))
  done
}

stop_painter() {
  if [ -z "$PAINTER" ]; then return 0; fi
  kill "$PAINTER" 2>/dev/null || true
  wait "$PAINTER" 2>/dev/null || true
  PAINTER=""
}

plain() {
  if [ -z "$FANCY" ]; then echo "$1"; fi
}

banner_lines() {
  if [ -n "$UTF8" ]; then
    cat <<'EOF'
███    ███ ██    ██ ███████ ██████   █████  ███████ ██   ██
████  ████ ██    ██ ██      ██   ██ ██   ██ ██      ██   ██
██ ████ ██ ██    ██ ███████ ██   ██ ███████ ███████ ███████
██  ██  ██ ██    ██      ██ ██   ██ ██   ██      ██ ██   ██
██      ██  ██████  ███████ ██████  ██   ██ ███████ ██   ██
EOF
  else
    cat <<'EOF'
    __  _____  _______ ____  ___   _____ __  __
   /  |/  / / / / ___// __ \/   | / ___// / / /
  / /|_/ / / / /\__ \/ / / / /| | \__ \/ /_/ /
 / /  / / /_/ /___/ / /_/ / ___ |___/ / __  /
/_/  /_/\____//____/_____/_/  |_/____/_/ /_/
EOF
  fi
}

banner_once() {
  banner_lines | while IFS= read -r bn_l; do
    printf '%s  %s%s%s\n' "$CLR" "$1" "$bn_l" "$C_0"
  done
}

# The name, fading in from grey to blue.
banner() {
  if [ -z "$COLOR" ]; then
    banner_once ""
    return 0
  fi
  bn_first=1
  for bn_c in $RAMP; do
    if [ -z "$bn_first" ]; then printf '%s' "$E[5A"; fi
    bn_first=""
    banner_once "$E[${bn_c}m"
    sleep 0.08 2>/dev/null || true
  done
}

# box draws the dashboard's address in a rectangle with three pointers in
# front of it. The second argument is the pointer that is lit, or 3 for all.
box() {
  bx_inner=$((3 + 3 + 2 + 9 + 4 + ${#1} + 3))
  bx_edge=$(rep "$G_H" "$bx_inner")
  bx_gap=$(rep " " "$bx_inner")
  bx_ptr="" bx_i=0
  while [ "$bx_i" -lt 3 ]; do
    if [ "$2" = 3 ] || [ "$2" = "$bx_i" ]; then
      bx_ptr="$bx_ptr$C_B$C_W$G_PTR$C_0"
    else
      bx_ptr="$bx_ptr$C_D$G_PTR$C_0"
    fi
    bx_i=$((bx_i + 1))
  done
  printf '  %s%s%s%s%s\n' "$C_B" "$G_TL" "$bx_edge" "$G_TR" "$C_0"
  printf '  %s%s%s%s%s%s\n' "$C_B" "$G_V" "$C_0" "$bx_gap" "$C_B$G_V" "$C_0"
  printf '  %s%s%s   %s  Dashboard    %s%s%s   %s%s%s\n' "$C_B" "$G_V" "$C_0" "$bx_ptr" "$C_U" "$1" "$C_0" "$C_B" "$G_V" "$C_0"
  printf '  %s%s%s%s%s%s\n' "$C_B" "$G_V" "$C_0" "$bx_gap" "$C_B$G_V" "$C_0"
  printf '  %s%s%s%s%s\n' "$C_B" "$G_BL" "$bx_edge" "$G_BR" "$C_0"
}

# The pointers run toward the address for a second and a half and then
# stand still: the script has to end.
show_box() {
  sb_k=0
  while [ "$sb_k" -lt 15 ]; do
    if [ "$sb_k" -gt 0 ]; then printf '%s' "$E[5A"; fi
    box "$1" $((sb_k % 3))
    sleep 0.1 2>/dev/null || true
    sb_k=$((sb_k + 1))
  done
  printf '%s' "$E[5A"
  box "$1" 3
}

# --- The steps -------------------------------------------------------------
#
# A step is a function that runs in this shell, so that what it finds out
# is there for the next one, with what it prints in the log. It is called
# as a condition, and inside a condition "set -e" is off: every command of
# a step that may fail ends in "|| return 1". What a step echoes before it
# returns 1 is what the person is shown.

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 2 --connect-timeout 15 -o "$2" "$1"
  else
    wget -q -O "$2" "$1"
  fi
}

probe() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsS -o /dev/null --max-time 2 "$1" 2>/dev/null
  else
    wget -q -O /dev/null -T 2 "$1" 2>/dev/null
  fi
}

step_check() {
  [ "$(uname -s)" = Linux ] || {
    echo "musdash runs on Linux, and this is $(uname -s)."
    return 1
  }
  command -v systemctl >/dev/null 2>&1 || {
    echo "musdash needs systemd, and this server has no systemctl."
    return 1
  }
  ARCH=$(arch_of "$(uname -m)") || {
    echo "There is no musdash for this processor ($(uname -m))."
    echo "It is built for amd64 (x86_64) and arm64 (aarch64)."
    return 1
  }
  command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1 || {
    echo "curl or wget is needed, and this server has neither."
    return 1
  }
  ck_os=$( (. /etc/os-release && echo "${NAME:-Linux} ${VERSION_ID:-}") 2>/dev/null) || ck_os=Linux
  OLD=""
  if [ -x "$BIN" ]; then
    OLD=$("$BIN" version 2>/dev/null | awk '{ print $2 }')
  fi
  # The architecture first: a long name of a system is cut at its end.
  DETAIL="$ARCH, ${ck_os:-Linux}"
}

install_git() {
  if command -v apt-get >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get install -y git ||
      { apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y git; }
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y git
  elif command -v yum >/dev/null 2>&1; then
    yum install -y git
  else
    return 1
  fi
}

step_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    echo "Docker is not installed: running Docker's own install script (https://get.docker.com)."
    fetch https://get.docker.com "$TMP/get-docker.sh" || {
      echo "Docker's install script could not be downloaded from https://get.docker.com."
      return 1
    }
    sh "$TMP/get-docker.sh" || {
      echo "Docker's install script failed. Install Docker by hand"
      echo "(https://docs.docker.com/engine/install/) and run this again."
      return 1
    }
    command -v docker >/dev/null 2>&1 || {
      echo "Docker's install script ended, and there is still no docker command."
      return 1
    }
  fi
  # The daemon is asked, not systemd: Docker from another package has a
  # service of another name, and what matters is that it answers.
  dk_n=0
  until docker info >/dev/null 2>&1; do
    if [ "$dk_n" -eq 0 ]; then systemctl enable --now docker >/dev/null 2>&1 || true; fi
    dk_n=$((dk_n + 1))
    if [ "$dk_n" -gt 20 ]; then
      echo "Docker is installed, but its daemon does not answer (docker info)."
      echo "See: systemctl status docker"
      return 1
    fi
    sleep 1
  done
  # Membership of this group is what lets musdash manage containers.
  getent group docker >/dev/null 2>&1 || groupadd --system docker || {
    echo "There is no docker group, and it could not be made."
    return 1
  }
  if ! command -v git >/dev/null 2>&1; then
    echo "git is not installed: installing it."
    install_git || NOTE_GIT=1
  fi
  DETAIL="Docker $(docker --version 2>/dev/null | awk '{ print $3 }' | tr -d ,)"
  if command -v git >/dev/null 2>&1; then
    DETAIL="$DETAIL, git $(git --version 2>/dev/null | awk '{ print $3 }')"
  fi
}

# A binary is tried from where it will live, under another name: /tmp is
# mounted noexec on servers that were hardened, and nothing runs from it.
step_download() {
  install -d /usr/local/bin || return 1
  MADE_NEW=1
  rm -f "$NEWBIN"
  if [ -n "$LOCAL_BIN" ]; then
    install -m 0755 "$LOCAL_BIN" "$NEWBIN" || return 1
    SKIP=1
  else
    dl_file="musdash-linux-$ARCH"
    dl_url=$(release_url "$dl_file")
    echo "Downloading $dl_url"
    fetch "$dl_url" "$TMP/$dl_file" || {
      echo "musdash could not be downloaded from $dl_url"
      return 1
    }
    fetch "$(release_url checksums.txt)" "$TMP/checksums.txt" || {
      echo "The release's checksums could not be downloaded, so the binary"
      echo "cannot be checked. Nothing was installed."
      return 1
    }
    dl_want=$(checksum_for "$dl_file" "$TMP/checksums.txt")
    [ -n "$dl_want" ] || {
      echo "The release's checksums have no line for $dl_file."
      echo "Nothing was installed."
      return 1
    }
    dl_got=$(sha256_of "$TMP/$dl_file") || {
      echo "sha256sum is needed to check the download, and this server has none."
      return 1
    }
    [ "$dl_want" = "$dl_got" ] || {
      echo "The download does not match its checksum. Nothing was installed."
      echo "  expected $dl_want"
      echo "  got      $dl_got"
      echo "This is usually a download that was cut short: run the command again."
      return 1
    }
    install -m 0755 "$TMP/$dl_file" "$NEWBIN" || return 1
  fi
  NEW=$("$NEWBIN" version 2>/dev/null | awk '{ print $2 }')
  [ -n "$NEW" ] || {
    echo "The binary does not run on this server. Is it for another processor?"
    return 1
  }
  DETAIL=$NEW
  if [ -n "$LOCAL_BIN" ]; then DETAIL="$NEW, from a file here"; fi
}

step_user() {
  if id musdash >/dev/null 2>&1; then
    DETAIL="already there"
  else
    useradd --system --home-dir "$DATA" --shell /usr/sbin/nologin musdash || return 1
  fi
  usermod -aG docker musdash || return 1
  install -d -m 0700 -o musdash -g musdash "$DATA" || return 1
}

# When the binary and both units are already what would be installed,
# nothing is touched and step_start restarts nothing: pasting the command a
# second time must not cost the apps the second the proxy's restart takes.
step_services() {
  unit_server >"$TMP/musdash-server.service" || return 1
  unit_proxy >"$TMP/musdash-proxy.service" || return 1
  CHANGED=""
  cmp -s "$NEWBIN" "$BIN" || CHANGED=1
  cmp -s "$TMP/musdash-server.service" "$UNITS/musdash-server.service" || CHANGED=1
  cmp -s "$TMP/musdash-proxy.service" "$UNITS/musdash-proxy.service" || CHANGED=1
  if [ -n "$CHANGED" ]; then
    mv -f "$NEWBIN" "$BIN" || return 1
    install -m 0644 "$TMP/musdash-server.service" "$UNITS/musdash-server.service" || return 1
    install -m 0644 "$TMP/musdash-proxy.service" "$UNITS/musdash-proxy.service" || return 1
    systemctl daemon-reload || return 1
  else
    rm -f "$NEWBIN"
    DETAIL="nothing changed"
  fi
  MADE_NEW=""
  systemctl enable musdash-proxy.service musdash-server.service >/dev/null 2>&1 || {
    echo "systemctl could not enable the two services."
    return 1
  }
}

step_start() {
  if [ -z "$CHANGED" ] &&
    systemctl is-active --quiet musdash-proxy.service &&
    systemctl is-active --quiet musdash-server.service; then
    SKIP=1
    DETAIL="already running"
    return 0
  fi
  # The proxy first: on an upgrade it is back in about a second, and apps
  # are unreachable only for that moment.
  systemctl restart musdash-proxy.service || {
    echo "The proxy did not start. Its last lines:"
    journalctl -u musdash-proxy -n 15 --no-pager 2>/dev/null || true
    return 1
  }
  systemctl restart musdash-server.service || {
    echo "The dashboard did not start. Its last lines:"
    journalctl -u musdash-server -n 15 --no-pager 2>/dev/null || true
    return 1
  }
}

step_wait() {
  wt_n=0
  while [ "$wt_n" -lt 30 ]; do
    if probe "http://127.0.0.1:$PORT/healthz"; then
      DETAIL="answering on port $PORT"
      return 0
    fi
    sleep 1
    wt_n=$((wt_n + 1))
  done
  echo "The dashboard did not answer on port $PORT within thirty seconds."
  echo "Its last lines:"
  journalctl -u musdash-server -n 15 --no-pager 2>/dev/null || true
  return 1
}

run_step() {
  rs_n=$1
  DETAIL=""
  SKIP=""
  set_state "$rs_n" run
  plain "[$rs_n/$STEPS] $(name_of "$rs_n")"
  printf '\n== step %s: %s\n' "$rs_n" "$(name_of "$rs_n")" >>"$LOG"
  LOGSTART=$(wc -l <"$LOG")
  # A step reads nothing: with "curl | sudo sh" this shell's input is the
  # script itself, and a command that read from it would swallow the rest.
  if "$2" >>"$LOG" 2>&1 </dev/null; then
    if [ -n "$SKIP" ]; then
      set_state "$rs_n" skip "${DETAIL:-not needed}"
      plain "[$rs_n/$STEPS] not needed${DETAIL:+: $DETAIL}"
    else
      set_state "$rs_n" ok "$DETAIL"
      plain "[$rs_n/$STEPS] done${DETAIL:+: $DETAIL}"
    fi
  else
    fail_step "$rs_n"
  fi
}

# A failed step is named in the list, and the end of what it printed is
# shown under it, so that the reason is on the screen and not only in a
# file.
fail_step() {
  read_state "$1"
  set_state "$1" fail "failed after $(($(date +%s) - T0))s"
  stop_painter
  plain "[$1/$STEPS] failed"
  echo
  tail -n "+$((LOGSTART + 1))" "$LOG" | tail -n 15 | while IFS= read -r fs_l; do
    printf '  %s\n' "$(clean "$fs_l" 200)"
  done
  echo
  echo "  The whole output is in $LOG"
  exit 1
}

# --- The last screen -------------------------------------------------------

ask_address() {
  if command -v curl >/dev/null 2>&1; then
    curl -4fsS --max-time 3 https://ipv4.icanhazip.com 2>/dev/null | tr -d ' \r\n'
  else
    wget -4 -q -T 3 -O - https://ipv4.icanhazip.com 2>/dev/null | tr -d ' \r\n'
  fi
}

# The address a browser can use: the one this server's outgoing route has,
# and when that one is private (a cloud server behind NAT knows no other),
# what one public service says it sees. Its answer is data: it is used only
# if it is an address.
public_address() {
  if [ -n "${MUSDASH_ADDRESS:-}" ]; then
    clean "$MUSDASH_ADDRESS" 60 | tr -d ' '
    return 0
  fi
  pa=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -n 1) || pa=""
  if [ -z "$pa" ]; then
    pa=$(hostname -I 2>/dev/null | awk '{ print $1 }') || pa=""
  fi
  if ! valid_ipv4 "$pa"; then pa=""; fi
  if [ -z "$pa" ] || is_private "$pa"; then
    pb=$(ask_address) || pb=""
    if valid_ipv4 "$pb" && ! is_private "$pb"; then pa=$pb; fi
  fi
  if [ -z "$pa" ]; then pa="<this server's address>"; fi
  echo "$pa"
}

finish() {
  fn_url="http://$(public_address):$PORT"
  if [ -n "$FANCY" ]; then
    echo
    banner
    echo
    show_box "$fn_url"
    echo
  else
    echo "musdash $NEW is running."
    echo "Dashboard: $fn_url"
  fi
  if [ -z "$OLD" ]; then
    echo "  Open it and create the owner account."
  elif [ "$OLD" != "$NEW" ]; then
    echo "  Upgraded from $OLD to $NEW. Your account and your apps are as they were."
  elif [ -z "$CHANGED" ]; then
    echo "  musdash $NEW was installed already. Nothing was restarted."
  else
    echo "  musdash $NEW was installed again."
  fi
  cat <<EOF

  Ports    80 and 443 for your apps, $PORT for the dashboard
           until you give it a domain under Settings
  Status   systemctl status musdash-server musdash-proxy
  Logs     journalctl -u musdash-server -f
  Data     $DATA  (back up musdash.db and master.key together)
EOF
  if [ -n "$NOTE_GIT" ]; then
    cat <<EOF

  git could not be installed here. Deploying from a Git repository needs
  it: install git with this system's package manager.
EOF
  fi
  # Small servers run out of memory during image builds. Swap turns a crash
  # into a slow build.
  fn_mem=$(awk '/MemTotal/ { print int($2 / 1024) }' /proc/meminfo 2>/dev/null) || fn_mem=""
  fn_swap=$(awk '/SwapTotal/ { print int($2 / 1024) }' /proc/meminfo 2>/dev/null) || fn_swap=""
  if [ -n "$fn_mem" ] && [ -n "$fn_swap" ] && [ "$fn_mem" -lt 2000 ] && [ "$fn_swap" -lt 500 ]; then
    cat <<EOF

  This server has $fn_mem MB of memory and no swap. Building images here can
  run out of memory. To add 2 GB of swap:

    fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap /swapfile
    swapon /swapfile && echo '/swapfile none swap sw 0 0' >> /etc/fstab
EOF
  fi
  echo
}

cleanup() {
  cu_rc=$?
  stop_painter
  if [ -n "$FANCY" ]; then printf '%s' "$E[?25h"; fi
  if [ -n "$MADE_NEW" ]; then rm -f "$NEWBIN" 2>/dev/null || true; fi
  if [ -n "$TMP" ]; then rm -rf "$TMP"; fi
  exit "$cu_rc"
}

main() {
  LOCAL_BIN=${1:-}
  if [ "$(id -u)" -ne 0 ]; then
    echo "install: this needs root. Run it with sudo:" >&2
    echo "  curl -fsSL https://github.com/$REPO/releases/latest/download/install.sh | sudo sh" >&2
    exit 1
  fi
  if [ -n "$LOCAL_BIN" ] && [ ! -f "$LOCAL_BIN" ]; then
    echo "install: $LOCAL_BIN is not a file. Usage: install.sh [a musdash binary for this machine]" >&2
    exit 1
  fi
  if [ -n "$VERSION" ] && ! valid_version "$VERSION"; then
    echo "install: a version looks like v1.2.3, not \"$(clean "$VERSION" 40)\"" >&2
    exit 1
  fi
  TMP=$(mktemp -d) || {
    echo "install: no temporary directory could be made" >&2
    exit 1
  }
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM HUP
  # The log may hold what a package manager printed. Only its own mode is
  # narrowed: a umask for the whole script would also be Docker's and
  # apt's, and what they install must stay readable.
  (umask 077 && : >"$LOG") 2>/dev/null || {
    echo "install: $LOG cannot be written" >&2
    exit 1
  }
  chmod 600 "$LOG"
  look
  mn_i=1
  while [ "$mn_i" -le "$STEPS" ]; do
    set_state "$mn_i" wait
    mn_i=$((mn_i + 1))
  done
  plain "musdash installer"
  start_painter
  run_step 1 step_check
  run_step 2 step_docker
  run_step 3 step_download
  run_step 4 step_user
  run_step 5 step_services
  run_step 6 step_start
  run_step 7 step_wait
  stop_painter
  finish
}

# Everything above is definitions. A download that stopped half way has no
# last line, so it runs nothing.
[ "${MUSDASH_INSTALL_LIB:-}" = 1 ] || main "$@"
