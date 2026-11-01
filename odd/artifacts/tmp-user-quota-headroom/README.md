# UID 1000 /tmp quota: leave at least 300 MB

**Installed locally, applied to the current mount, and independently verified.**
The parent installed the two root-owned files below and read back both limits as
`7578271` blocks, leaving `300000256` bytes outside UID 1000's allowance. The
existing instance loaded the post-start hook without restarting active sessions.
A restrictive creation mask initially made the two new directories `0700`;
the parent corrected only those verified task-created directories to `0755`.
Both installed files are root-owned `0644` and match the reviewed source hashes. No reboot or disruptive login test has been performed.
Only the parent performs privileged maintenance; offline tests never apply quota.

## Review and installation boundary

| Reviewed source | Installed root-owned destination (parent maintenance only) |
|---|---|
| `tmp-user-quota-1000.py` | `/usr/local/libexec/tmp-user-quota-1000.py` |
| `50-tmp-quota-headroom.conf` | `/etc/systemd/system/user-runtime-dir@1000.service.d/50-tmp-quota-headroom.conf` |

The intended invocation is `/usr/bin/python3 -I -S -B /usr/local/libexec/tmp-user-quota-1000.py`,
as root, without arguments. **That invocation mutates quota: it is not an offline
check or a read-only diagnostic.** Importing the module does not run it. Do not
execute the staged helper against the real kernel during preparation. The helper
uses no subprocess, user/account lookup, home content, or environment configuration.

The parent confirmed systemd v262's default quota is applied inside the existing
`user-runtime-dir@1000.service` start operation. The instance-specific
`ExecStartPost` runs afterward and reapplies this policy on normal reboot/login
resource starts. It needs no new service, account record, global quota setting,
or mount-size change. Installing a drop-in alone does not change an already
active instance; the parent owns current application without restarting active
user resources. The leading `-` deliberately preserves login if a future helper
invocation fails; **unit success alone is not quota-success evidence**.

## Fixed policy and safety bounds

- Linux x86_64, effective UID 0 only. Open only `/tmp`, with `O_DIRECTORY`,
  `O_NOFOLLOW`, and `O_CLOEXEC`; use that same descriptor for filesystem and quota
  operations. `fstatfs` must report tmpfs magic `0x01021994`; its aligned 256-byte
  buffer exceeds the x86_64 statfs size. Capacity is `fstatvfs.f_blocks * f_frsize`,
  not available/free space.
- Reserve **300,000,000 bytes** (decimal MB). Both soft and hard block limits are
  `floor((capacity - 300000000) / 1024)`; require a positive result and current
  usage no greater than its byte equivalent. For capacity `8060149760`, both
  limits are `7578271` blocks (`7760149504` bytes), leaving `300000256` bytes.
- Use only `quotactl_fd`, syscall **443**, UID **1000**, user quota type **0**.
  Verified public headers: `/usr/include/asm/unistd_64.h` defines
  `__NR_quotactl_fd 443`; `/usr/include/linux/quota.h` defines Q_GETQUOTA
  `0x800007`, Q_SETQUOTA `0x800008`, command shift 8, 1024-byte quota blocks,
  `if_dqblk` (eight u64 fields followed by u32 validity), and `QIF_BLIMITS = 1`.
  The ctypes ABI has size 72, validity offset 64 on x86_64.
- Set validity **1** only: both block limits, no inode limits, usage counters, or
  grace-time fields. Read back both actual block limits and confirm both inode
  limits match the pre-write values. Other UIDs are never addressed.
- All initial GET errors, **including ESRCH**, fail closed; no missing-entry
  assumption is needed for the parent's observed existing quota. No retries,
  fallback mechanism, or automatic rollback. A post-write readback error or
  mismatch is an error but may leave the requested quota applied; the parent
  must inspect it before any recovery. Do not lower limits blindly into active
  allocations.

This reserves headroom in UID 1000's allowance, **not globally free /tmp space or
extra/free RAM**. Other users can consume space. Future unrelated root quota
writers, mount replacement/remounts, capacity changes, or lifecycle changes are
outside this persistence guarantee. The usage check and write are not atomic;
concurrent allocations and administrative changes require live verification.
No reboot test has been performed, and offline success proves no live policy.

## Verified outcome

Independent verification met S1 and S2: a fresh read-only own-UID kernel query
confirmed both limits and unchanged inode limits; installed bytes, root ownership,
file/directory permissions, the effective instance hook, and the unchanged mount
unit matched expectations. All 12 offline tests and three additional synthetic
boundary/reapplication probes passed. The extra probes were not added as project
files. Systemd validation passed without output.

Persistence is verified as configuration for the documented normal reboot/login
resource lifecycle—not as an observed reboot or logout test. Those disruptive
checks were intentionally not performed. The existing active instance's start
timestamp stayed unchanged. Initial directory-permission failure and its bounded
correction remain recorded in `odd/tasks/tmp-user-quota-headroom.md`.

## Diagnostics and offline checks

Success emits one compact JSON line with only numeric quota/capacity values:
`uid`, `total_bytes`, `reserve_bytes`, `block_soft`, `block_hard`,
`headroom_bytes`, `inode_soft`, `inode_hard`. Failure exits nonzero, with stderr
and no success JSON. Expected errors use `error: <guard>` or `error: errno=N`.
The parent can inspect the existing unit's journal stderr and success JSON, then
independently read the live quota. Do not infer success from the ignored-exit
post-hook or substitute old proof outcomes for fresh verification.

From the repository root, the focused **offline** command is:

```sh
TMPDIR=/tmp/restaurant-quota-apply-1sOzOz/staging-AxCuLC TMP=/tmp/restaurant-quota-apply-1sOzOz/staging-AxCuLC TEMP=/tmp/restaurant-quota-apply-1sOzOz/staging-AxCuLC python3 -I -S -B -m unittest discover -s odd/artifacts/tmp-user-quota-headroom -p test_tmp_user_quota.py -v
```

That run uses stdlib unittest only. Tests import the helper and invoke its public
`main` with synthetic root/architecture/filesystem/statvfs and libc boundaries;
no real GET or SET quota syscall occurs. They cover exact rounding, minimum
positive capacity, usage equality/overflow, rejection without quota mutation,
syscall failures, readback mismatch, one SET with mask 1, inode preservation,
fixed UID/path, resource closure, and inert import. The supplied private staging
directory must already exist; the command creates no directory or cache.
