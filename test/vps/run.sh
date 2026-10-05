#!/bin/sh
# usage: ./run.sh script.py   (MSD_SSH points at a wrapper that runs its argument on the VPS)
export MSD_SSH=${MSD_SSH:-$HOME/.musdash-vps-ssh}
cd "$(dirname "$0")" && exec python3 -u "$@"
