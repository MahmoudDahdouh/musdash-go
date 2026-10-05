"""Second tester's entry point: same harness, own results file (out/results-b.jsonl), `t2-` prefix."""
import os
os.environ.setdefault("MSD_SSH", os.path.expanduser("~/.musdash-vps-ssh"))
import lib
lib.RESULTS = lib.RESULTS.replace("results.jsonl", "results-b.jsonl")
from lib import *
