# Changelog

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
