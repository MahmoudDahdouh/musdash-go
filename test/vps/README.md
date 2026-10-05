# test/vps: end-to-end tests against a real VPS

Stdlib-only Python harness used for `docs/testing/vps-test-report.md`. It drives a running musdash over HTTP (forms are filled from the page itself, with the CSRF token), the API, signed webhooks, WebSockets, and runs ground-truth commands on the server over SSH.

```sh
export MSD_BASE=http://<vps>:8000 MSD_HOST=<vps ip> MSD_SSH=/path/to/script-that-runs-its-argument-on-the-vps
./run.sh s00_install.py      # one suite; results append to out/results.jsonl
python3 make_report.py       # writes docs/testing/vps-test-report.md (needs findings.md and report_head.md)
```

* `lib.py` holds the client, form helpers, TOTP, a WebSocket client and `rec/check` for results. `out/state.json` keeps ids and credentials between scripts (git-ignored with the rest of `out/`).
* Scripts are named `sNN<letter>_<topic>.py` after the suites in `docs/testing/vps-full-test-plan.md` and are mostly run in order; later ones reuse the apps, database and tokens earlier ones created.
* Some scripts are destructive on purpose (`kill -9`, `systemctl restart docker`, reboot, installer re-run): run them only on a throwaway VPS.
* `ui_sweep_node.js` and `ui_interact_node.js` are Playwright scripts (they need `playwright` and a Chrome binary; edit the paths at the top).
* The browser sweep saves its screenshots to `docs/testing/screenshots/`, which is git-ignored like `out/`.
