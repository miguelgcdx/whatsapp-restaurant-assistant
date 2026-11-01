"""Apply the fixed UID 1000 /tmp block-quota policy (Linux x86_64 only)."""
import ctypes
import json
import os
import sys


class Dqblk(ctypes.Structure):
    _fields_ = [(name, ctypes.c_uint64) for name in (
        "bhardlimit", "bsoftlimit", "curspace", "ihardlimit", "isoftlimit",
        "curinodes", "btime", "itime",
    )] + [("valid", ctypes.c_uint32)]


# Linux UAPI: asm/unistd_64.h (__NR_quotactl_fd), linux/quota.h.
NR_QUOTACTL_FD = 443
Q_GETQUOTA = 0x800007 << 8  # QCMD(..., USRQUOTA=0)
Q_SETQUOTA = 0x800008 << 8
RESERVE = 300000000


def checked(result):
    if result == -1:
        raise OSError(ctypes.get_errno(), "kernel call failed")


def apply():
    if os.geteuid() != 0:
        raise ValueError("root required")
    host = os.uname()
    if host.sysname != "Linux" or host.machine != "x86_64" or ctypes.sizeof(ctypes.c_void_p) != 8:
        raise ValueError("Linux x86_64 required")
    libc = ctypes.CDLL(None, use_errno=True)
    libc.fstatfs.argtypes = [ctypes.c_int, ctypes.c_void_p]
    libc.fstatfs.restype = ctypes.c_int
    # Variadic syscall: fixed c_long syscall number; remaining values below are
    # bounded C int arguments and a ctypes pointer (never a truncated address).
    libc.syscall.argtypes = [ctypes.c_long]
    libc.syscall.restype = ctypes.c_long
    fd = os.open("/tmp", os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        # x86_64 statfs is 120 bytes; 256 aligned bytes safely hold all fields.
        fs = (ctypes.c_long * 32)()
        checked(libc.fstatfs(fd, ctypes.byref(fs)))
        if fs[0] != 0x01021994:
            raise ValueError("/tmp must be tmpfs")
        capacity = os.fstatvfs(fd)
        total = capacity.f_blocks * capacity.f_frsize
        target = (total - RESERVE) // 1024
        if target <= 0:
            raise ValueError("capacity cannot preserve reserve")
        before = Dqblk()
        checked(libc.syscall(NR_QUOTACTL_FD, fd, Q_GETQUOTA, 1000, ctypes.byref(before)))
        if before.curspace > target * 1024:
            raise ValueError("usage exceeds target")
        desired = Dqblk()
        desired.bhardlimit = desired.bsoftlimit = target
        desired.valid = 1  # QIF_BLIMITS: both block limits, NOT inode limits.
        checked(libc.syscall(NR_QUOTACTL_FD, fd, Q_SETQUOTA, 1000, ctypes.byref(desired)))
        after = Dqblk()
        checked(libc.syscall(NR_QUOTACTL_FD, fd, Q_GETQUOTA, 1000, ctypes.byref(after)))
        if (after.bhardlimit, after.bsoftlimit, after.ihardlimit, after.isoftlimit) != (
                target, target, before.ihardlimit, before.isoftlimit):
            raise ValueError("quota readback mismatch")
        return dict(uid=1000, total_bytes=total, reserve_bytes=RESERVE,
                    block_soft=after.bsoftlimit, block_hard=after.bhardlimit,
                    headroom_bytes=total - after.bhardlimit * 1024,
                    inode_soft=after.isoftlimit, inode_hard=after.ihardlimit)
    finally:
        os.close(fd)


def main():
    try:
        result = apply()
    except (OSError, ValueError) as error:
        message = "errno=" + str(error.errno) if isinstance(error, OSError) else str(error)
        print("error: " + message, file=sys.stderr)
        return 1
    print(json.dumps(result, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    sys.exit(main())
