#!/bin/bash
# Retired startup search: recent git commit messages do not identify the user's
# new session task and injected unrelated historical chunks at SessionStart.
# Keep cached registrations silent; Stop indexing (session-index.sh) and
# on-demand /recall remain active.
cat >/dev/null || true
exit 0
