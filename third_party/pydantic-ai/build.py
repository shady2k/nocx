"""Build the locked local wheel without modifying installed packages.

Run with the manifest's CPython and uv on PATH:
  python third_party/pydantic-ai/build.py
Uses hash-locked build requirements, GNU-compatible patch, and a temporary venv.
SOURCE_DATE_EPOCH and the explicit version make wheel bytes reproducible.
"""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile


HERE = Path(__file__).resolve().parent


def checked(path, digest):
    if hashlib.sha256(path.read_bytes()).hexdigest() != digest:
        raise RuntimeError(f'Hash mismatch: {path.name}')


def main():
    if sys.version_info[:3] != (3, 13, 16):
        raise RuntimeError('Build requires pinned CPython 3.13.16')
    if subprocess.check_output(['uv', '--version'], text=True).split()[1] != '0.13.0':
        raise RuntimeError('Build requires pinned uv 0.13.0')
    provenance = json.loads((HERE / 'PROVENANCE.json').read_text())
    upstream = provenance['upstream']
    archive = HERE / upstream['sdist']
    patch = HERE / 'unknown-tool-handler.patch'
    checked(archive, upstream['sha256'])
    checked(patch, provenance['patch_sha256'])
    with tempfile.TemporaryDirectory(prefix='nocx-pydantic-build-') as directory:
        work = Path(directory)
        with tarfile.open(archive) as source:
            source.extractall(work, filter='data')
        source_dir = work / 'pydantic_ai_slim-2.52.0'
        subprocess.run(['patch', '--batch', '--fuzz=0', '-p1', '-i', str(patch)], cwd=source_dir, check=True)
        venv = work / 'venv'
        subprocess.run(['uv', 'venv', '--python', sys.executable, str(venv)], check=True)
        python = venv / 'bin/python'
        subprocess.run([
            'uv', 'pip', 'sync', '--python', str(python), '--require-hashes',
            '--index-url', 'https://pypi.org/simple', str(HERE / 'build-requirements.txt'),
        ], check=True)
        environment = dict(os.environ, SOURCE_DATE_EPOCH=str(provenance['source_date_epoch']),
                           **provenance['version_environment'])
        subprocess.run([
            str(python), '-m', 'hatchling', 'build', '--target', 'wheel',
            '--directory', str(HERE / 'dist'),
        ], cwd=source_dir, env=environment, check=True)
        wheel = HERE / 'dist/pydantic_ai_slim-2.52.0+nocx.1-py3-none-any.whl'
        if 'wheel_sha256' in provenance:
            checked(wheel, provenance['wheel_sha256'])
        print(f'{hashlib.sha256(wheel.read_bytes()).hexdigest()}  {wheel.name}')


if __name__ == '__main__':
    main()
