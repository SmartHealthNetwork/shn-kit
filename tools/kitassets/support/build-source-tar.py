#!/usr/bin/env python3
"""Rebuild source.tar.gz deterministically from gateway/deploy/validator/support.

The Kit ships the validation support package with its full source (inputs, generator, tests,
closure walk and the committed closure members) so a partner can reproduce it. Run from the
repository root after any change under gateway/deploy/validator/support:

    python3 tools/kitassets/support/build-source-tar.py

test/validatorhealth's vendored-parity test compares the archive's members with the tree.
"""
import gzip
import io
import os
from pathlib import Path
import tarfile

ROOT = Path(__file__).resolve().parents[3]
SRC = ROOT / 'gateway' / 'deploy' / 'validator' / 'support'
OUT = ROOT / 'tools' / 'kitassets' / 'support' / 'source.tar.gz'

files = []
for directory, dirnames, filenames in os.walk(SRC):
    dirnames[:] = sorted(d for d in dirnames if d != '__pycache__')
    for name in filenames:
        path = Path(directory) / name
        files.append(path.relative_to(SRC).as_posix())
files.sort()
buffer = io.BytesIO()
with gzip.GzipFile(fileobj=buffer, mode='wb', filename='', mtime=0) as gz:
    with tarfile.open(fileobj=gz, mode='w', format=tarfile.USTAR_FORMAT) as tar:
        for relative in files:
            data = (SRC / relative).read_bytes()
            info = tarfile.TarInfo(relative)
            info.size, info.mode, info.mtime = len(data), 0o644, 0
            tar.addfile(info, io.BytesIO(data))
OUT.write_bytes(buffer.getvalue())
print('source.tar.gz members: %d' % len(files))
