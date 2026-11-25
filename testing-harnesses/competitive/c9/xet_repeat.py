"""Repeat the successful exact, shifted and entropy lanes on disposable OpenXet."""
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "c1"))
import xet
from local_xet import main

xet.CASES = tuple(row for row in xet.CASES if not row[0].startswith("tensor"))

if __name__ == "__main__":
    main()
