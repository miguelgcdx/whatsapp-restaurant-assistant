"""Offline public-main tests: all host and libc boundaries are synthetic."""
import contextlib
import ctypes
import errno
import importlib.util
import io
import json
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch


HELPER = Path(__file__).with_name("tmp-user-quota-1000.py")


def load_helper():
    spec = importlib.util.spec_from_file_location("quota_helper", HELPER)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class QuotaTests(unittest.TestCase):
    def setUp(self):
        self.m = load_helper()
        self.state = dict(bhardlimit=6296992, bsoftlimit=6296992,
                          curspace=57749504, ihardlimit=12345, isoftlimit=12000,
                          curinodes=23, btime=0, itime=0, valid=63)
        self.calls = []
        self.sets = []
        self.get_error = None
        self.set_error = None
        self.readback_bad = None
        self.fs_magic = 0x01021994
        self.fs_error = False
        self.total = 8060149760
        self.libc = Mock()
        self.libc.fstatfs.side_effect = self.fstatfs
        self.libc.syscall.side_effect = self.syscall

    def fstatfs(self, fd, ptr):
        self.assertEqual(fd, 17)
        if self.fs_error:
            ctypes.set_errno(errno.EIO)
            return -1
        ctypes.cast(ptr, ctypes.POINTER(ctypes.c_long))[0] = self.fs_magic
        return 0

    def syscall(self, nr, fd, cmd, uid, ptr):
        self.assertEqual((nr, fd, uid), (443, 17, 1000))
        self.calls.append(cmd)
        q = ctypes.cast(ptr, ctypes.POINTER(self.m.Dqblk)).contents
        if cmd == 0x800007 << 8:
            if self.get_error:
                ctypes.set_errno(self.get_error)
                return -1
            for key, value in self.state.items():
                setattr(q, key, value)
            if self.sets and self.readback_bad:
                setattr(q, self.readback_bad, 42)
            return 0
        self.assertEqual(cmd, 0x800008 << 8)
        self.sets.append({name: getattr(q, name) for name, _ in q._fields_})
        if self.set_error:
            ctypes.set_errno(self.set_error)
            return -1
        self.assertEqual(q.valid, 1)
        self.state.update(bhardlimit=q.bhardlimit, bsoftlimit=q.bsoftlimit)
        return 0

    def run_main(self, root=0, machine="x86_64", system="Linux", open_error=None,
                 stat_error=None):
        out, err = io.StringIO(), io.StringIO()
        before = self.state.copy()
        with contextlib.ExitStack() as stack:
            stack.enter_context(patch.object(self.m.os, "geteuid", return_value=root))
            stack.enter_context(patch.object(self.m.os, "uname", return_value=SimpleNamespace(
                sysname=system, machine=machine)))
            opened = stack.enter_context(patch.object(self.m.os, "open", return_value=17,
                                                      side_effect=open_error))
            closed = stack.enter_context(patch.object(self.m.os, "close"))
            stack.enter_context(patch.object(self.m.os, "fstatvfs", side_effect=stat_error,
                return_value=SimpleNamespace(f_blocks=self.total, f_frsize=1)))
            loader = stack.enter_context(patch.object(self.m.ctypes, "CDLL", return_value=self.libc))
            stack.enter_context(contextlib.redirect_stdout(out))
            stack.enter_context(contextlib.redirect_stderr(err))
            code = self.m.main()
        if opened.called:
            opened.assert_called_once_with("/tmp", self.m.os.O_RDONLY | self.m.os.O_DIRECTORY
                                           | self.m.os.O_NOFOLLOW | self.m.os.O_CLOEXEC)
            if not open_error:
                closed.assert_called_once_with(17)
        if root != 0 or machine != "x86_64" or system != "Linux":
            opened.assert_not_called()
            loader.assert_not_called()
        if not self.sets:
            self.assertEqual(self.state, before)
        return code, out.getvalue(), err.getvalue(), opened, loader

    def reject(self, message, **kwargs):
        code, out, err, _, _ = self.run_main(**kwargs)
        self.assertEqual((code, out, err), (1, "", "error: " + message + "\n"))
        self.assertEqual(self.sets, [])

    def test_import_is_inert(self):
        with patch("ctypes.CDLL", side_effect=AssertionError("libc on import")), \
                patch("os.open", side_effect=AssertionError("open on import")):
            load_helper()

    def test_success_public_main_exact_math_abi_and_preservation(self):
        code, out, err, _, _ = self.run_main()
        self.assertEqual((code, err), (0, ""))
        expected = dict(uid=1000, total_bytes=8060149760, reserve_bytes=300000000,
                        block_soft=7578271, block_hard=7578271,
                        headroom_bytes=300000256, inode_soft=12000, inode_hard=12345)
        self.assertEqual(out, json.dumps(expected, separators=(",", ":")) + "\n")
        self.assertEqual(self.calls, [0x800007 << 8, 0x800008 << 8, 0x800007 << 8])
        self.assertEqual(len(self.sets), 1)
        self.assertEqual(self.sets[0], dict(bhardlimit=7578271, bsoftlimit=7578271,
                         curspace=0, ihardlimit=0, isoftlimit=0, curinodes=0,
                         btime=0, itime=0, valid=1))
        self.assertEqual(self.state, dict(bhardlimit=7578271, bsoftlimit=7578271,
                         curspace=57749504, ihardlimit=12345, isoftlimit=12000,
                         curinodes=23, btime=0, itime=0, valid=63))
        self.assertEqual(ctypes.sizeof(self.m.Dqblk), 72)
        self.assertEqual(self.m.Dqblk.valid.offset, 64)

    def test_root_and_architecture_guards(self):
        for kwargs, message in [({"root": 1000}, "root required"),
                                ({"machine": "aarch64"}, "Linux x86_64 required"),
                                ({"machine": "x86_64", "system": "Other"}, "Linux x86_64 required")]:
            with self.subTest(kwargs=kwargs):
                self.reject(message, **kwargs)
                self.assertEqual(self.calls, [])

    def test_wrong_filesystem(self):
        self.fs_magic = 0xEF53
        self.reject("/tmp must be tmpfs")
        self.assertEqual(self.calls, [])

    def test_capacity_boundaries(self):
        for total in [0, 299999999, 300000000, 300001023]:
            with self.subTest(total=total):
                self.total = total
                self.reject("capacity cannot preserve reserve")
        self.assertEqual(self.calls, [])

    def test_smallest_positive_target_and_usage_equality(self):
        self.total = 300001024
        self.state["curspace"] = 1024
        code, out, err, _, _ = self.run_main()
        self.assertEqual((code, err), (0, ""))
        self.assertEqual(json.loads(out)["block_hard"], 1)
        self.assertEqual(json.loads(out)["headroom_bytes"], 300000000)
        self.assertEqual(len(self.sets), 1)

    def test_over_target_rejected_without_write(self):
        self.state["curspace"] = 7760149505
        self.reject("usage exceeds target")

    def test_get_errors_including_esrch_fail_closed(self):
        for number in [errno.ESRCH, errno.EPERM, errno.ENOSYS, errno.EIO]:
            with self.subTest(errno=number):
                self.get_error = number
                self.reject("errno=" + str(number))

    def test_filesystem_and_open_errors(self):
        self.reject("errno=40", open_error=OSError(errno.ELOOP, "synthetic"))
        self.reject("errno=5", stat_error=OSError(errno.EIO, "synthetic"))
        self.fs_error = True
        self.reject("errno=5")
        self.assertEqual(self.calls, [])

    def test_set_failure_no_retry(self):
        self.set_error = errno.EPERM
        before = self.state.copy()
        code, out, err, _, _ = self.run_main()
        self.assertEqual((code, out, err), (1, "", "error: errno=1\n"))
        self.assertEqual(self.state, before)
        self.assertEqual(len(self.sets), 1)
        self.assertEqual(len(self.calls), 2)

    def test_readback_mismatch_no_retry_or_rollback(self):
        for field in ["bhardlimit", "bsoftlimit", "ihardlimit", "isoftlimit"]:
            with self.subTest(field=field):
                self.setUp()
                self.readback_bad = field
                code, out, err, _, _ = self.run_main()
                self.assertEqual((code, out, err), (1, "", "error: quota readback mismatch\n"))
                self.assertEqual(len(self.sets), 1)
                self.assertEqual(len(self.calls), 3)

    def test_readback_error_no_retry(self):
        original = self.syscall
        def fail_readback(*args):
            if self.sets:
                self.get_error = errno.EIO
            return original(*args)
        self.libc.syscall.side_effect = fail_readback
        code, out, err, _, _ = self.run_main()
        self.assertEqual((code, out, err), (1, "", "error: errno=5\n"))
        self.assertEqual(len(self.sets), 1)
        self.assertEqual(len(self.calls), 3)


if __name__ == "__main__":
    unittest.main()
