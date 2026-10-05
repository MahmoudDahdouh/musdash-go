#!/bin/sh
cd "$(dirname "$0")"
for s in s14_api.py s13_shared.py s05b_apps.py s05c_apps.py s05d_recheck.py s05e_rollback.py s16a_sec.py s16b_sec.py s16c_secrets.py s15_terminal.py s15b_leak.py s11a_tasks.py s07b_hooks.py s07d_prev.py s09a_backup.py s08b_db.py; do
  echo "=== $s"; ./run.sh $s 2>&1 | tail -60
done
echo ALLDONE
