#!/bin/sh
# Runs inside one container as root: install, run, rotate, upgrade, remove
# (test plan PK-01 to PK-03). Usage: check.sh <rpm|deb> <old package> <new package> <old version> <new version>
set -u
kind=$1 old=$2 new=$3 old_version=$4 new_version=$5
failures=0

pass() { echo "PASS $1"; }
fail() { echo "FAIL $1${2:+: $2}"; failures=$((failures + 1)); }
check() { # name, command...
    name=$1; shift
    if output=$("$@" 2>&1); then pass "$name"; else fail "$name" "$output"; fi
}
mode_is() { # path, mode (octal as stat prints it)
    [ "$(stat -c %a "$1" 2>/dev/null)" = "$2" ]
}

install_package() {
    if [ "$kind" = rpm ]; then rpm -U "$1"; else DEBIAN_FRONTEND=noninteractive dpkg --force-confold -i "$1"; fi
}
remove_package() {
    if [ "$kind" = rpm ]; then rpm -e jevtri; else dpkg -r jevtri; fi
}
install_logrotate() { # gzip: missing in almalinux/8-minimal and needed by compress; util-linux: script(1) for RP-01
    if command -v microdnf >/dev/null; then microdnf -y install logrotate gzip ca-certificates tzdata util-linux >/dev/null
    elif command -v dnf >/dev/null; then dnf -y install logrotate gzip ca-certificates tzdata util-linux >/dev/null
    else apt-get update >/dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y logrotate gzip ca-certificates tzdata util-linux >/dev/null
    fi
}

# Minimized Ubuntu images drop /usr/share/man from every package; the test is
# about what the package holds, so let dpkg install it as on a full system.
rm -f /etc/dpkg/dpkg.cfg.d/excludes

# rpm -U and dpkg -i do not resolve dependencies, so prerequisites are
# installed explicitly here; standard-manager resolution is tested separately.
if [ "$kind" = rpm ]; then deps=$(rpm -qpR "$old"); else deps=$(dpkg-deb -f "$old" Depends); fi
check "PK-01 depends on gzip" sh -c 'echo "$0" | grep -qw gzip' "$deps"
if [ "$kind" = rpm ]; then new_deps=$(rpm -qpR "$new"); else new_deps=$(dpkg-deb -f "$new" Depends); fi
check "PK-01 new package depends on ca-certificates" sh -c 'printf "%s\n" "$0" | tr ", " "\n\n" | grep -Fxq ca-certificates' "$new_deps"
for dependency in logrotate tzdata; do
    check "PK-01 new package depends on $dependency" sh -c 'printf "%s\n" "$0" | tr ", " "\n\n" | grep -Fxq "$1"' "$new_deps" "$dependency"
done
# Standard-manager resolution must pass before test prerequisites can mask gaps.
install_resolved() {
    if [ "$kind" = rpm ]; then
        if ! command -v dnf >/dev/null; then microdnf -y install dnf || return; fi
        dnf -y install "$new"
    else
        apt-get update || return
        DEBIAN_FRONTEND=noninteractive apt-get install -y "$new"
    fi
}
if install_resolved; then
    for dependency in gzip ca-certificates logrotate tzdata; do
        if [ "$kind" = rpm ]; then
            check "PK-DEP resolved $dependency" rpm -q "$dependency"
        else
            check "PK-DEP resolved $dependency" sh -c 'dpkg -s "$1" | grep -qx "Status: install ok installed"' sh "$dependency"
        fi
    done
    check "PK-DEP logrotate executable" sh -c 'command -v logrotate'
    check "PK-DEP CA bundle" sh -c 'test -s /etc/ssl/certs/ca-certificates.crt || test -s /etc/pki/tls/certs/ca-bundle.crt'
    check "PK-DEP IANA timezone data" test -f /usr/share/zoneinfo/Asia/Tokyo
    check "PK-DEP resolved version" sh -c 'jevtri --version | grep -qx "jevtri $1"' sh "$new_version"
    check "PK-DEP remove preflight candidate" remove_package
else
    fail "PK-DEP standard-manager installation failed"
    exit 1
fi
install_logrotate
# PK-01: files, modes, and the command runs.
check "PK-01 install" install_package "$old"
check "PK-01 /usr/bin/jevtri executable" test -x /usr/bin/jevtri
check "PK-01 example config 0644" mode_is /usr/share/jevtri/jevtri.conf.example 644
check "PK-01 logrotate config 0644" mode_is /etc/logrotate.d/jevtri 644
check "PK-01 /etc/jevtri exists" test -d /etc/jevtri
check "PK-01 /var/log/jevtri 0700" mode_is /var/log/jevtri 700
check "PK-01 no jevtri.conf packaged" test ! -e /etc/jevtri/jevtri.conf
check "PK-01 --help" jevtri --help
check "PK-01 man pages (en, ja, zh_CN) 0644" sh -c 'for p in /usr/share/man/man1 /usr/share/man/ja/man1 /usr/share/man/zh_CN/man1; do [ "$(stat -c %a $p/jevtri.1)" = 644 ] || exit 1; done'
check "LG-01 --help --lang zh-CN" sh -c "jevtri --help --lang zh-CN | grep -q '概率'"
check "LG-01 --help with LANG=ja_JP.UTF-8" sh -c "LANG=ja_JP.UTF-8 jevtri --help | grep -q '確率'"
check "PK-01 --version is $old_version" sh -c "jevtri --version | grep -qx 'jevtri $old_version'"
example_loads() {
    # The example's logs are usually missing in a container, which also exits 1,
    # so a config error is told apart by its "<file>:<line>:" message.
    errors=$(jevtri -c /usr/share/jevtri/jevtri.conf.example --dry-run </dev/null 2>&1 >/dev/null)
    code=$?
    if printf '%s\n' "$errors" | grep -q 'jevtri\.conf\.example:[0-9]'; then
        printf '%s\n' "$errors"; return 1
    fi
    [ "$code" -ne 1 ] || printf '%s\n' "$errors" | grep -q 'none of the configured logs could be evaluated' || { printf '%s\n' "$errors"; return 1; }
}
check "PK-01 example loads (--dry-run)" example_loads

# A config with one log that has lines in the window, run without a terminal.
now=$(date '+%b %e %H:%M:%S')
printf '%s host app[1]: started\n%s host app[1]: error: disk full\n' "$now" "$now" > /tmp/app.log
printf '[log app]\npath = /tmp/app.log\ntime_format = syslog\n' > /etc/jevtri/jevtri.conf
check "PK-01 --dry-run exits 0" sh -c 'jevtri --dry-run </dev/null >/tmp/dry.out'
check "PK-01 --dry-run shows the log" grep -q 'disk full' /tmp/dry.out
check "PK-01 sent.log 0600" mode_is /var/log/jevtri/sent.log 600

# PK-03: logrotate reads the config and rotates with the stated settings.
if install_logrotate; then
    check "PK-03 logrotate -d" logrotate -d /etc/logrotate.d/jevtri
    debug=$(logrotate -d /etc/logrotate.d/jevtri 2>&1)
    check "PK-03 daily" sh -c "echo \"\$0\" | grep -qi 'rotating pattern: /var/log/jevtri/sent.log .*after 1 days'" "$debug"
    check "PK-03 30 rotations" sh -c "echo \"\$0\" | grep -q '(30 rotations)'" "$debug"
    check "PK-03 forced rotation" logrotate -f -s /tmp/logrotate.state /etc/logrotate.d/jevtri
    check "PK-03 rotated file kept" test -s /var/log/jevtri/sent.log.1
    check "PK-03 new sent.log 0600 root" sh -c '[ "$(stat -c "%a %U %G" /var/log/jevtri/sent.log)" = "600 root root" ]'
    # delaycompress: the first rotation stays plain, the second is gzipped.
    jevtri --dry-run </dev/null >/dev/null 2>&1
    check "PK-03 second rotation" logrotate -f -s /tmp/logrotate.state /etc/logrotate.d/jevtri
    check "PK-03 compressed" sh -c 'gzip -t /var/log/jevtri/sent.log.2.gz && test -s /var/log/jevtri/sent.log.1'
else
    fail "PK-03 logrotate could not be installed; checks not run"
fi

# RP-01: jevtri report from the installed package. A run that Jev answered is
# written into the send log by hand (no API key here); script(1) gives report
# the terminal it requires.
answered() { # time, host
    printf '{"time":"%s","dry_run":false,"request":{"state":{"window_start":"a","window_end":"b","symptom":"down on %s","logs":{"/tmp/app.log":"%s failed to reach 192.0.2.44"}}},"model":"jev-test","scores":[{"log":"/tmp/app.log","score":2.4}]}\n' "$1" "$2" "$2"
}
host=$(cat /proc/sys/kernel/hostname)  # hostname(1) is missing in the minimal images
[ -n "$host" ] || fail "RP-01 no host name"
answered 2026-09-28T10:00:00+09:00 "$host" >> /var/log/jevtri/sent.log.1
answered 2026-09-29T10:00:00+09:00 "$host" >> /var/log/jevtri/sent.log
if command -v script >/dev/null; then
    check "RP-01 report runs" sh -c "printf '2\n1\nconnection limit\n' | script -qec 'jevtri report' /dev/null > /tmp/report.out"
    check "RP-01 older run from sent.log.1 listed" grep -q '2026-09-28 10:00' /tmp/report.out
    check "RP-01 issue URL printed" grep -q 'https://github.com/takeshiue/jevtri/issues/new?.*template=case.yml' /tmp/report.out
    case_file=$(ls /var/log/jevtri/case-*.md 2>/dev/null | head -1)
    check "RP-01 report file 0600" mode_is "$case_file" 600
    check "RP-01 cause and OS recorded" sh -c "grep -q '^- <code>/tmp/app.log</code>' '$case_file' && grep -q '^- OS: <code>.' '$case_file'"
    check "RP-01 host name and IP replaced" sh -c "grep -q 'failed to reach \[ip-1\]' '$case_file' && ! grep -qF '$host' '$case_file' && ! grep -q '192.0.2.44' '$case_file'"
    [ "$failures" -eq 0 ] || { echo "--- report output"; cat /tmp/report.out; echo "--- $case_file"; cat "$case_file"; }
else
    fail "RP-01 script(1) is missing; checks not run"
fi

# This smoke runs only in the disposable installation container, never a host.
new_configuration_smoke() {
    sh -s <<'PACKAGE_SMOKE'
    set -eu
    mode_is() { [ "$(stat -c %a "$1" 2>/dev/null)" = "$2" ]; }
    same_bytes() {
        left=$(sha256sum "$1") || return 1
        right=$(sha256sum "$2") || return 1
        test "${left%% *}" = "${right%% *}"
    }
    smoke=$(mktemp -d /tmp/jevtri-package.XXXXXX)
    chmod 700 "$smoke"
    stub_created=no
    cleanup_smoke() {
        if [ "$stub_created" = yes ]; then rm -f /usr/local/bin/docker; fi
        rm -rf "$smoke"
    }
    trap cleanup_smoke EXIT HUP INT TERM
    printf '2026-10-04 00:00:00 package fixture\n' > "$smoke/app.log"
    chmod 600 "$smoke/app.log"
    printf '[general]\nminutes = 7\nsent_log = %s/audit.log\n[log package-smoke]\npath = %s/app.log\ntime_format = iso-space\ngroup = smoke\nmask = SHOW-PACKAGE-PRIVATE-CANARY\n' "$smoke" "$smoke" > "$smoke/config.conf"
    chmod 600 "$smoke/config.conf"
    cp "$smoke/config.conf" "$smoke/config.original"
    cp "$smoke/app.log" "$smoke/app.original"
    for language in en ja zh-CN; do
        jevtri --help --lang "$language" > "$smoke/help-$language.out"
        grep -q '/etc/jevtri/jevtri.conf' "$smoke/help-$language.out"
        grep -q -- '--show' "$smoke/help-$language.out"
    done
    for directory in /usr/share/man/man1 /usr/share/man/ja/man1 /usr/share/man/zh_CN/man1; do
        grep -q '/etc/jevtri/jevtri.conf' "$directory/jevtri.1"
        grep -q 'show' "$directory/jevtri.1"
    done
    jevtri --show -c "$smoke/config.conf" > "$smoke/show.out"
    jevtri --show -c "$smoke/config.conf" --json > "$smoke/show.json"
    grep -qF "$smoke/config.conf" "$smoke/show.out"
    grep -qF "$smoke/app.log" "$smoke/show.out"
    grep -q '"config_file":' "$smoke/show.json"
    grep -q '"mask_rules": 1' "$smoke/show.json"
    ! grep -q SHOW-PACKAGE-PRIVATE-CANARY "$smoke/show.out" "$smoke/show.json"
    same_bytes "$smoke/config.conf" "$smoke/config.original"
    mode_is "$smoke/config.conf" 600
    test ! -e "$smoke/audit.log"
    result=0
    jevtri --show -c "$smoke/config.conf" --dry-run > "$smoke/mixed.out" 2> "$smoke/mixed.err" || result=$?
    test "$result" = 64 && test ! -s "$smoke/mixed.out"
    result=0
    jevtri --show -c "$smoke/missing.conf" > "$smoke/missing.out" 2> "$smoke/missing.err" || result=$?
    test "$result" = 1 && test ! -s "$smoke/missing.out"
    test ! -e "$smoke/missing.conf"
    # A known-empty local inventory permits a separate deletion decision.
    # Refuse to replace any existing CLI, even a nonexecutable or broken link.
    for existing in /usr/bin/docker /bin/docker /usr/local/bin/docker; do
        if [ -e "$existing" ] || [ -L "$existing" ]; then
            printf 'Docker CLI already exists; package fixture refuses to overwrite it\n' >&2
            exit 1
        fi
    done
    mkdir -p /usr/local/bin
    cat > /usr/local/bin/docker <<'DOCKER_STUB'
#!/bin/sh
case "$1 $2 $3" in
    'context show ') printf 'default\n' ;;
    'context inspect --format') printf '"unix:///var/run/docker.sock"\n' ;;
    '--host unix:///var/run/docker.sock info') printf '"/var/lib/jevtri-package-docker"\n' ;;
    '--host unix:///var/run/docker.sock container') test "$4" = ls ;;
    *) exit 64 ;;
esac
DOCKER_STUB
    chmod 755 /usr/local/bin/docker
    stub_created=yes
    # Package-manager logs vary by distro; count offered additions and decline
    # them before selecting the one synthetic registration for removal.
    index=0
    while [ "$index" -lt 100 ]; do printf '\n'; index=$((index + 1)); done > "$smoke/probe.in"
    script -qec "jevtri --config-update -c '$smoke/config.conf'" /dev/null < "$smoke/probe.in" > "$smoke/probe.out"
    grep -q 'Registrations to remove' "$smoke/probe.out"
    ! grep -q 'Docker discovery is unavailable' "$smoke/probe.out"
    fresh_count=$(grep -o 'Add it?' "$smoke/probe.out" | wc -l)
    test "$fresh_count" -lt 100
    index=0
    while [ "$index" -lt "$fresh_count" ]; do printf 'n\n'; index=$((index + 1)); done > "$smoke/remove.in"
    printf 'all\ny\n\n' >> "$smoke/remove.in"
    script -qec "jevtri --config-update -c '$smoke/config.conf'" /dev/null < "$smoke/remove.in" > "$smoke/remove.out"
    ! grep -q '^\[log package-smoke\]' "$smoke/config.conf"
    grep -q 'Delete log file' "$smoke/remove.out"
    same_bytes "$smoke/app.log" "$smoke/app.original"
    mode_is "$smoke/app.log" 600
    cp "$smoke/config.original" "$smoke/config.conf"
    index=0
    while [ "$index" -lt "$fresh_count" ]; do printf 'n\n'; index=$((index + 1)); done > "$smoke/delete.in"
    printf 'all\ny\ny\n' >> "$smoke/delete.in"
    script -qec "jevtri --config-update -c '$smoke/config.conf'" /dev/null < "$smoke/delete.in" > "$smoke/delete.out"
    ! grep -q '^\[log package-smoke\]' "$smoke/config.conf"
    grep -qF "Deleted log file $smoke/app.log." "$smoke/delete.out"
    test ! -e "$smoke/app.log"
    mode_is "$smoke/config.conf" 600
    test ! -e "$smoke/audit.log"
    printf 'show/default-keep/explicit-delete smoke passed with synthetic Docker inventory\n'
PACKAGE_SMOKE
}

# PK-02: changed settings survive an upgrade and removal.
printf '# changed by the test\n' >> /etc/logrotate.d/jevtri
printf 'dummy\n' > /etc/jevtri/api-key && chmod 600 /etc/jevtri/api-key
check "PK-02 upgrade" install_package "$new"
check "PK-02 --version is $new_version" sh -c "jevtri --version | grep -qx 'jevtri $new_version'"
check "PK-04 upgraded show and explicit registration/file deletion smoke" new_configuration_smoke
# The release version must run after the package manager has replaced it.
now=$(date '+%b %e %H:%M:%S')
printf '%s host app[1]: error: disk full password=PACKAGE-TEST-CANARY\n' "$now" > /tmp/app.log
check "PK-02 upgraded dry-run exits 0" sh -c 'jevtri --dry-run </dev/null >/tmp/upgraded-dry.out'
check "PK-02 upgraded dry-run reads log" grep -q 'disk full' /tmp/upgraded-dry.out
check "PK-02 upgraded dry-run masks secret" sh -c '! grep -q PACKAGE-TEST-CANARY /tmp/upgraded-dry.out && grep -q MASKED /tmp/upgraded-dry.out'
check "PK-02 upgraded rejects unknown group" sh -c 'jevtri --dry-run --group unknown </dev/null >/tmp/unknown-group.out 2>/tmp/unknown-group.err; result=$?; [ "$result" = 64 ] && [ ! -s /tmp/unknown-group.out ]'
check "PK-02 changed logrotate kept on upgrade" grep -q 'changed by the test' /etc/logrotate.d/jevtri
check "PK-02 jevtri.conf kept on upgrade" grep -q 'path = /tmp/app.log' /etc/jevtri/jevtri.conf
check "PK-02 remove" remove_package
check "PK-02 binary removed" test ! -e /usr/bin/jevtri
check "PK-02 jevtri.conf kept on removal" test -f /etc/jevtri/jevtri.conf
check "PK-02 api-key kept on removal" test -f /etc/jevtri/api-key
check "PK-02 changed logrotate kept on removal" sh -c 'grep -qs "changed by the test" /etc/logrotate.d/jevtri /etc/logrotate.d/jevtri.rpmsave'
check "PK-02 sent.log kept on removal" test -f /var/log/jevtri/sent.log

echo "failures: $failures"
[ "$failures" -eq 0 ]
