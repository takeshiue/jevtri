# Changelog

## Unreleased

## 0.2.0 - 2026-10-03

- Reject Compose configuration migration before any changes when an existing
  container registration has custom mask rules, individual settings or comments,
  preserving its configuration.
- Mask entire quoted secret values and Digest authorization credentials; keep
  confidential contents out of public-check diagnostics.
- Reject missing scores and ambiguous log identities before sending; stop further
  ranking requests when the first-stage send record fails; stop time-format
  detection before sending when its record is unavailable or after a write failure.
- Apply max_bytes to the serialized JSON state, omit empty groups, accept configured
  quiet groups and distinguish first-stage excerpts from final log rankings.
- Reject FIFO and unsafe parent symlinks for send logs, validate timezone offsets,
  preserve raw-score ranking order and repair configuration discovery/completion.

- `jevtri init` starts by asking for the Jev API key (paste and Enter) and
  stores it in `/etc/jevtri/api-key` with mode 0600.
- Verify and save each Docker container's actual absolute json-file log path,
  with container and Compose metadata and an explicit `time_format`. Normal runs
  read the saved paths; review recreation or storage changes with `--config-update`.
- Read the systemd journal through journalctl (`journal_unit`, or `*` for all).
- `jevtri --config-update` looks for logs and containers that are not in the
  configuration and asks about each (`y`, `all`, or Enter to skip), then asks
  whether to remove logs whose file or container is gone.
- New time format `docker-json`.
- `init` and `--config-update` offer Docker Compose projects together and save
  one entry per container. Updates preserve individual masks, formats, groups
  and comments. Added containers are discovered only during configuration updates.
- Approve verified Docker path and format changes with `all`, and review missing
  groups' defaults together. Missing registered log files produce a short warning
  in English, Japanese or Chinese with the configuration update command.
- Support end-of-line `#` comments and quoted values in configuration. Include an
  inactive writing sample at the top, preserving users' comments during updates.
- Apply each registration's custom masks before time-format previews, requests
  and records, including multiple registrations of the same file.
- `group` puts a log in one or more groups. `init` and `--config-update`
  write `system` for host logs and the Docker Compose project for containers.
  Without `--group`, when two or more groups of services have lines in the
  window, a run first asks Jev which group to examine, then ranks that
  group's logs with the system logs. `--group NAME` chooses the group.
- The default send limit is 40,000 bytes (was 48,000): measured against Jev,
  logs made only of access log lines were refused above about 41,600 bytes.

## 0.1.0 - 2026-10-01

First release.

- Rank the configured logs by investigation priority (0-100) for an incident
  time, with the Jev API. Text, verbose (`-v`) and JSON (`--json`) output.
- Mask secrets before sending; show what would be sent with `--dry-run`; record
  every run in `/var/log/jevtri/sent.log`.
- `jevtri init` finds known logs, checks their time formats, and asks Jev to
  identify unknown formats with your consent.
- Eleven time formats and user-defined `%` patterns; rotated and `.gz` files;
  file name patterns for logs with dates in their names.
- `jevtri report` writes a report of the real cause of a past run, with the
  host name, people's user names and IP addresses replaced, for the public issue form. It sends
  nothing.
- rpm and deb packages for x86_64 and arm64.
