# PaNasMs Backup and Restore

Administrator-only encrypted backups, separate from updater rollback, filesystem
snapshots and Cloud Sync. The module uses restic repositories and the shared
PaNasMs module host, maintenance lock, identity checks and interface components.

## Destinations

- Local disks and mounted SMB/NFS shares, including Synology. Connect network
  shares in Storage first. Mount and repository identities are checked before use.
- Google Drive and Dropbox through explicit PaNasMs account grants. Provider refresh
  tokens remain in the core; temporary rclone credentials are removed after use.
- Existing private GitHub repositories, **platform configuration only**, up to
  32 MiB per encrypted archive. Independent archives are draft release assets,
  with a checksum/completion receipt. Payloads do not enter Git history.

Choose a repository password and keep it outside the NAS. Existing restic
repositories can be reconnected using their original password. A password stored
only on the lost system is not a recovery strategy. GitHub recovery additionally
requires an account with write access to its private draft releases.

## Sources and consistency

| Source | Method and boundary |
| --- | --- |
| Files/folders | Versioned file copy, not an atomic application snapshot. Select each mounted filesystem separately. |
| SQLite | SQLite online backup API and integrity check, including committed WAL contents. |
| PostgreSQL container | `pg_dumpall`; consistent individual databases, not one transaction across databases. Uses the container's configured database user. |
| MySQL/MariaDB container | Logical dump with global table locks, including routines/events/triggers. Database writes may wait. Uses the container's root-password environment. |
| Docker application | Explicit downtime consent; stop every selected writer, preserve image/root filesystem/configuration and local bind/volume data, restart before repository upload. Other running containers sharing writable storage cause rejection. |
| Platform | Online platform SQLite copies, selected system configuration and package/storage inventory, plus a reviewed replacement-system recovery helper. |

Application and platform capture use the exclusive PaNasMs maintenance lock.
Other active NAS jobs must finish first. Application restart intent is journaled
before stopping a container and recovered after module restart. Container IDs and
volume identities are pinned in the plan. Do not modify selected data with tools
outside PaNasMs during the consistency capture.

Intervals are measured in hours; zero means manual runs. Overdue enabled plans
run after the NAS returns. Jobs are serialized. Retention keeps the union of last,
daily and weekly versions and never runs after an incomplete backup. Repository
checks after backup verify structure; **Verify all data** reads encrypted payloads.
Integrity verification is distinct from an actual recovery rehearsal.

## Restore

Select a working disk in Recovery for temporary application copies and restored files.
The module pins its mount identity and stops operations if less than 2 GiB remains;
provision room for the full application copy. Existing restores stay on their original disk.

Browse a version and select an individual path or the complete version. Restores
always use a new private directory and verify file content. A restored individual
file can be downloaded from History. Existing files are never overwritten by the
module's normal restore action.

Application restoration recreates separate stopped containers with restored
writable filesystems and volumes. Network access, automatic restart and published
ports are disabled. Review the copies in Containers before starting; hardware,
privileged workloads, Compose orchestration and external services need separate
review. Partial restored files and created containers remain available after an
error; they are not silently deleted.

A platform backup is **not a bootable disk image**. On a replacement installation,
install the same OS/PaNasMs version, reconnect the repository, verify it and restore
its platform backup. The archive includes `recover.py` next to `recovery.json`:

```sh
sudo python3 recover.py plan --backup platform --target /
sudo python3 recover.py apply --backup platform --target / --confirm-replacement
```

Run `apply` only on the intended replacement installation after reviewing the plan.
It stops PaNasMs services, preserves replaced files, applies platform configuration
and databases, then restarts services. A failed apply leaves services stopped and
prints the rollback directory. The helper intentionally preserves fresh-system
boot/mount/network/identity settings and module registry. Recreate Linux accounts
with their original IDs, reconnect/unlock data disks and reinstall matching signed
modules separately. Inspect `reviewSeparately` in the plan; do not overwrite disk
UUIDs or network settings blindly. Physical media replacement and each target OS
need their own recorded boot/recovery acceptance.

## Development

Go 1.27, Node 24, Python 3, restic and rclone. Run:

```sh
sh scripts/build.sh
```

Tests with restic installed exercise encrypted backup, literal filename restore,
retention, repository import and full payload verification. Additional tests cover
mount loss, path overlap, SQLite WAL and interrupted jobs. Cloud provider end-to-end
acceptance requires separately authorized test accounts; unit tests are not a claim
of live provider verification. UI acceptance across languages, themes, keyboard,
mobile and assistive technology is tracked independently of compilation.

The build emits unsigned ARM64/AMD64 source payloads. Install only signed packages
published through the PaNasMs module registry. Original code is under SUL-1.0; see
LICENSE and NOTICE.
