# Project agent instructions

## Project-local Gentle Shell

The optional `./scripts/gentle-shell-local [arguments...]` wrapper still uses
repository-local temporary storage; it is not required for new sessions.
It resolves the repository dynamically and invokes the `gentle-shell`
executable on `PATH`, not a shell function. Missing or unsafe storage fails
without fallback; launch directories are retained, not automatically cleaned.
This does not retrofit the live parent's environment, modify global shell
hooks, or itself retry any stopped verification. Existing sessions remain unchanged.

## Temporary storage

- Use `/tmp` for new task scratch, test databases, and verification copies.
- Create private directories with permissions `0700`. Do not adopt a symlink
  or a directory owned by another user.
- Create a unique private subdirectory for each run. Set `TMPDIR` to its absolute
  path explicitly for every relevant command, subprocess, and subagent, including
  commands that replace rather than inherit the environment.
- A typical shell setup is:

  ```sh
  RUN_TMP="$(mktemp -d /tmp/restaurant-assistant-XXXXXX)" || exit 1
  export TMPDIR="$RUN_TMP" TMP="$RUN_TMP" TEMP="$RUN_TMP"
  ```

- The shell example does not change the environment of later independent tool
  calls. Reapply the absolute `TMPDIR` in those calls and pass it to subagents.
- Do not relocate `HOME` or tool caches unless the task requires isolated values.
- If a tool ignores `TMPDIR` or requires another temporary location, explain the
  exception before proceeding. Do not assume this policy is a system sandbox.
- Shut down owned processes before removing their disposable run files. Never
  delete another run's files, retained diagnostic evidence, or pre-existing data.
- Temporary-location changes do not authorize retrying a previously stopped
  verification, changing resource limits, or deleting old `/tmp` evidence.
