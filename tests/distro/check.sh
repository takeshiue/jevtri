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
    if command -v microdnf >/dev/null; then microdnf -y install logrotate gzip util-linux >/dev/null
    elif command -v dnf >/dev/null; then dnf -y install logrotate gzip util-linux >/dev/null
    else apt-get update >/dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y logrotate gzip util-linux >/dev/null
    fi
}

# Minimized Ubuntu images drop /usr/share/man from every package; the test is
# about what the package holds, so let dpkg install it as on a full system.
rm -f /etc/dpkg/dpkg.cfg.d/excludes

# PK-01: gzip is a declared dependency. rpm -U and dpkg -i do not fetch
# dependencies, so it is installed first, as dnf or apt would.
if [ "$kind" = rpm ]; then deps=$(rpm -qpR "$old"); else deps=$(dpkg-deb -f "$old" Depends); fi
check "PK-01 depends on gzip" sh -c 'echo "$0" | grep -qw gzip' "$deps"
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

# PK-02: changed settings survive an upgrade and removal.
printf '# changed by the test\n' >> /etc/logrotate.d/jevtri
printf 'dummy\n' > /etc/jevtri/api-key && chmod 600 /etc/jevtri/api-key
check "PK-02 upgrade" install_package "$new"
check "PK-02 --version is $new_version" sh -c "jevtri --version | grep -qx 'jevtri $new_version'"
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
