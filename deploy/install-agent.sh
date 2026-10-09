#!/usr/bin/env bash
# Standalone installer: checksum failures stop before privileged writes.
set -Eeuo pipefail
umask 077

fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
run_root() { if [[ "$(id -u)" -eq 0 ]]; then "$@"; else sudo "$@"; fi; }
check_url() {
  [[ "$1" =~ ^https://[A-Za-z0-9.:/_?=\&+-]+$ ]] && return 0
  if [[ "${ALLOW_INSECURE:-0}" == 1 && "$1" =~ ^http://[A-Za-z0-9.:/_?=\&+-]+$ ]]; then return 0; fi
  fail 'HTTPS is required; HTTP requires explicit --allow-insecure (not a TLS verification bypass)'
}
download() {
  local url="$1" out="$2" protocols='=https'
  if [[ -n "${GH_PROXY:-}" && "$url" == https://github.com/* ]]; then
    [[ "$GH_PROXY" == https://* ]] || fail 'GitHub proxy must use HTTPS'
    url="${GH_PROXY%/}/$url"
  fi
  check_url "$url"
  [[ "${ALLOW_INSECURE:-0}" != 1 ]] || protocols='=http,https'
  command -v curl >/dev/null || fail 'curl is required'
  curl --fail --silent --show-error --location --retry 3 --tlsv1.2 --proto "$protocols" --proto-redir "$protocols" -o "$out" "$url" || return 1
  [[ -s "$out" ]]
}
resolve_release() {
  local repo="$1" version="$2" resolved
  if [[ "$version" == latest ]]; then
    resolved="$(curl --fail --silent --show-error --location --head --tlsv1.2 --proto '=https' --proto-redir '=https' -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest")" || fail 'Cannot resolve release version'
    [[ "$resolved" == "https://github.com/$repo/releases/tag/"* ]] || fail 'Unexpected release redirect'
    version="${resolved##*/}"
  fi
  [[ "$version" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ && "$version" != latest ]] || fail 'Invalid release version'
  printf '%s\n' "$version"
}
checksum_for() {
  local manifest="$1" asset="$2" digest name extra found='' count=0
  [[ -f "$manifest" && ! -L "$manifest" ]] || fail 'Checksum manifest is missing or a symlink'
  while read -r digest name extra || [[ -n "$digest" ]]; do
    digest="${digest%$'\r'}"; name="${name%$'\r'}"; extra="${extra%$'\r'}"
    [[ -n "$digest" ]] || continue
    [[ "$digest" =~ ^[0-9a-fA-F]{64}$ && -n "$name" && -z "$extra" ]] || fail 'Malformed checksum manifest'
    name="${name#\*}"
    if [[ "$name" == "$asset" ]]; then found="$digest"; count=$((count + 1)); fi
  done < "$manifest"
  [[ "$count" == 1 ]] || fail 'Checksum entry is missing or duplicated'
  printf '%s\n' "$(printf '%s' "$found" | tr 'ABCDEF' 'abcdef')"
}
verify_sha256() {
  local file="$1" expected="$2" actual
  [[ -f "$file" && ! -L "$file" && "$expected" =~ ^[0-9a-fA-F]{64}$ ]] || fail 'A regular file and exact SHA-256 are required'
  if command -v sha256sum >/dev/null; then actual="$(sha256sum "$file")"
  elif command -v shasum >/dev/null; then actual="$(shasum -a 256 "$file")"
  else fail 'sha256sum or shasum is required'; fi
  actual="${actual%% *}"
  [[ "$(printf '%s' "$actual" | tr 'ABCDEF' 'abcdef')" == "$(printf '%s' "$expected" | tr 'ABCDEF' 'abcdef')" ]] || fail 'SHA-256 mismatch; refusing installation'
}
verified_release_download() {
  local repo="$1" version="$2" asset="$3" out="$4" expected="${5:-}" manifest_name="${6:-SHA256SUMS}" manifest
  version="$(resolve_release "$repo" "$version")"
  download "https://github.com/$repo/releases/download/$version/$asset" "$out" || return 1
  if [[ -z "$expected" ]]; then
    manifest="$WORK/checksums-$asset"
    download "https://github.com/$repo/releases/download/$version/$manifest_name" "$manifest" || fail 'Release checksum download failed'
    expected="$(checksum_for "$manifest" "$asset")"
    printf '%s\n' 'NOTICE: TLS same-release checksums verify integrity, not an independent publisher signature.' >&2
  fi
  verify_sha256 "$out" "$expected"
}
verify_binary_magic() {
  local file="$1" os="$2" magic
  if [[ "$os" == windows ]]; then
    magic="$(od -An -tx1 -N2 "$file" | tr -d ' \n')"
    [[ "$magic" == 4d5a ]] || fail 'Not a Windows PE binary'
  else
    magic="$(od -An -tx1 -N4 "$file" | tr -d ' \n')"
    case "$os" in
      linux) [[ "$magic" == 7f454c46 ]] || fail 'Not a Linux ELF binary';;
      darwin) [[ "$magic" == cffaedfe || "$magic" == feedfacf || "$magic" == cafebabe ]] || fail 'Not a Mach-O binary';;
      *) fail 'Unsupported OS';;
    esac
  fi
}
detect_platform() {
  case "$(uname -s)" in
    Linux) PLATFORM=linux;; Darwin) PLATFORM=darwin;; MINGW*|MSYS*|CYGWIN*) PLATFORM=windows;; *) fail 'Unsupported OS';;
  esac
  case "$(uname -m)" in
    x86_64|amd64) GOARCH=amd64;; aarch64|arm64) GOARCH=arm64;; *) fail 'Unsupported architecture';;
  esac
}
validate_path() { [[ "$1" =~ ^/[A-Za-z0-9/_-]+$ && "$1" != / && "$1" != *'..'* ]] || fail 'Use a safe absolute installation path'; }
new_workdir() {
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/virtualis-install.XXXXXXXX")"
  trap 'rm -rf -- "$WORK"' EXIT
}

validate_mode() {
  case "$1" in
    1|2|4) ;;
    3) fail 'Mode 3 (LXD-compatible client) is unsupported: no validated LXD runtime integration; traditional LXC CLI is not compatible';;
    *) fail 'Unknown mode: use 1=Agent, 2=Incus, 3=LXD-compatible client (unsupported), 4=QEMU';;
  esac
}
install_backend() {
  validate_mode "$1"
  [[ "$1" != 1 ]] || return 0
  [[ "$PLATFORM" == linux ]] || fail 'Automatic Incus/QEMU backend installation is Linux-only'
  command -v systemctl >/dev/null || fail 'Backend installation requires systemd'
  case "$1" in
    2)
      if command -v apt-get >/dev/null; then run_root apt-get update; run_root apt-get install -y incus
      elif command -v dnf >/dev/null; then run_root dnf install -y incus
      else fail 'Unsupported package manager; install and initialize Incus manually'; fi
      run_root systemctl enable --now incus
      run_root incus info >/dev/null || fail 'Incus is not initialized; initialize it explicitly and retry'
      ;;
    4)
      if command -v apt-get >/dev/null; then run_root apt-get update; run_root apt-get install -y qemu-kvm qemu-utils libvirt-clients libvirt-daemon-system
      elif command -v dnf >/dev/null; then run_root dnf install -y qemu-kvm qemu-img libvirt
      else fail 'Unsupported package manager; install QEMU/libvirt manually'; fi
      run_root systemctl enable --now libvirtd
      run_root virsh list >/dev/null || fail 'libvirt is unavailable'
      ;;
  esac
}
agent_main() {
  local MASTER="${AGENT_MASTER_DEFAULT:-}" TOKEN_FILE='' NAME='' MODE=1 ADVERTISE=''
  local VERSION=latest EXPECTED_SHA256='' NO_START=0 DEST='/opt/virtualis/agent'
  GH_PROXY=''; ALLOW_INSECURE=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --master|--master-url) MASTER="${2:?master URL required}"; shift 2;;
      --token-file) TOKEN_FILE="${2:?token file required}"; shift 2;;
      --token) fail 'Use --token-file; command-line secrets are not accepted';;
      --allow-insecure) ALLOW_INSECURE=1; shift;;
      --name) NAME="${2:?name required}"; shift 2;;
      --mode) MODE="${2:?mode required}"; shift 2;;
      --advertise) ADVERTISE="${2:?advertise URL required}"; shift 2;;
      --version) VERSION="${2:?version required}"; shift 2;;
      --expected-sha256) EXPECTED_SHA256="${2:?SHA-256 required}"; shift 2;;
      --gh-proxy) GH_PROXY="${2:?proxy URL required}"; shift 2;;
      --no-start) NO_START=1; shift;; --update) shift;;
      -h|--help) printf '%s\n' 'Usage: --master-url https://MASTER --token-file /secure/token [--mode 1|2|4] [--allow-insecure] [--expected-sha256 independent-digest]'; return;;
      *) fail "Unknown option: $1";;
    esac
  done
  validate_mode "$MODE"
  [[ -n "$MASTER" && -n "$TOKEN_FILE" ]] || fail 'Both --master-url and --token-file are required'
  check_url "$MASTER"
  [[ -z "$ADVERTISE" ]] || check_url "$ADVERTISE"
  [[ -z "$EXPECTED_SHA256" || "$EXPECTED_SHA256" =~ ^[0-9a-fA-F]{64}$ ]] || fail 'Invalid expected SHA-256'
  [[ -f "$TOKEN_FILE" && ! -L "$TOKEN_FILE" && -s "$TOKEN_FILE" ]] || fail 'Token must be a nonempty regular file'
  NAME="${NAME:-node-$(hostname -s)}"
  [[ "$NAME" =~ ^[A-Za-z0-9._-]{1,63}$ ]] || fail 'Invalid node name'
  # Reject whitespace/control characters before constructing service arguments.
  local token; token="$(<"$TOKEN_FILE")"
  [[ "$token" =~ ^[A-Za-z0-9._~-]+$ ]] || fail 'Invalid token file contents'
  unset token
  detect_platform
  [[ "$PLATFORM" != windows || "$GOARCH" == amd64 ]] || fail 'Windows Agent supports amd64 only'
  [[ "$MODE" == 1 || "$PLATFORM" == linux ]] || fail 'This backend is Linux-only'
  new_workdir
  local asset="virtualis-agent-$PLATFORM-$GOARCH" suffix='' binary="$WORK/agent" checksum="$WORK/SHA256SUMS"
  [[ "$PLATFORM" != windows ]] || suffix=.exe
  asset+="$suffix"
  if ! verified_release_download SakuraOpenSource/virtualis-agent "$VERSION" "$asset" "$binary" "$EXPECTED_SHA256"; then
    # Only a failed binary transfer may use fallback. A checksum error exits above.
    [[ "$MASTER" != http://* || -n "$EXPECTED_SHA256" ]] || fail 'HTTP fallback requires an independently supplied expected SHA-256'
    download "$MASTER/api/agent/binary?os=$PLATFORM&arch=$GOARCH" "$binary" || fail 'Master binary download failed'
    if [[ -z "$EXPECTED_SHA256" ]]; then
      download "$MASTER/api/agent/binary?os=$PLATFORM&arch=$GOARCH&checksum=1" "$checksum" || fail 'Master checksum download failed'
      EXPECTED_SHA256="$(checksum_for "$checksum" "$asset")"
      printf '%s\n' 'NOTICE: Same-master TLS checksum is integrity checking, not an independent publisher signature.' >&2
    fi
    verify_sha256 "$binary" "$EXPECTED_SHA256"
  fi
  verify_binary_magic "$binary" "$PLATFORM"
  [[ "$PLATFORM" != windows ]] || fail 'PE checksum/magic verified; use install-virtualis.cmd for Windows token-file ACL handling'
  [[ "$PLATFORM" != linux ]] || command -v systemctl >/dev/null || fail 'systemd is required'
  install_backend "$MODE"
  run_root install -d -o root -m 0700 "$DEST" "$DEST/data"
  if [[ "$PLATFORM" == linux ]] && systemctl is-active --quiet virtualis-agent; then run_root systemctl stop virtualis-agent; fi
  run_root install -o root -m 0755 "$binary" "$DEST/virtualis-agent"
  run_root install -o root -m 0600 "$TOKEN_FILE" "$DEST/token"
  local insecure_arg='' advertise_arg=''
  [[ "$ALLOW_INSECURE" != 1 ]] || insecure_arg=' --allow-insecure'
  [[ -z "$ADVERTISE" ]] || advertise_arg=" --advertise $ADVERTISE"
  if [[ "$PLATFORM" == linux ]]; then
    # Agent intentionally retains root for hypervisor and network operations.
    run_root tee /etc/systemd/system/virtualis-agent.service >/dev/null <<EOF
[Unit]
Description=Virtualis Agent
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
User=root
UMask=0077
WorkingDirectory=$DEST
ExecStart=$DEST/virtualis-agent --master $MASTER --token-file $DEST/token --name $NAME --data $DEST/data$insecure_arg$advertise_arg
Restart=on-failure
RestartSec=5
[Install]
WantedBy=multi-user.target
EOF
    run_root systemctl daemon-reload
    run_root systemctl enable virtualis-agent
    if [[ "$NO_START" == 0 ]]; then run_root systemctl restart virtualis-agent; run_root systemctl is-active --quiet virtualis-agent || fail 'Agent did not start'; fi
  else
    printf '%s\n' "Verified binary: $DEST/virtualis-agent (launchd setup unsupported; use --token-file $DEST/token)"
  fi
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then agent_main "$@"; fi
