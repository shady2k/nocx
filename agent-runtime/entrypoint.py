"""Launch with packaged CPython: python -I -B /absolute/bundle/app/entrypoint.py.

Layout: bundle/app/{entrypoint.py,nocx_agent_runtime/}, and locked dependencies
in bundle/python/lib/python3.13/site-packages. No cwd, user-site or environment
import path participates. No runtime installation or network bootstrap occurs.
"""
import argparse
import asyncio
import json
import logging
from pathlib import Path
import sys


class LaunchParser(argparse.ArgumentParser):
    def error(self, message):
        raise ValueError('Invalid launch arguments')


class Diagnostics(logging.Handler):
    def emit(self, record):
        # The host receives only bounded structured categories, never raw SDK
        # messages (which may contain response bodies or credentials).
        sys.stderr.write(json.dumps({'kind': 'sdk_diagnostic', 'level': record.levelname.lower()}) + '\n')


def main():
    if not sys.flags.isolated or not sys.dont_write_bytecode or sys.version_info[:3] != (3, 13, 16):
        sys.stderr.write('{"kind":"runtime_error","code":"launch"}\n')
        return 2
    parser = LaunchParser(add_help=False, exit_on_error=False)
    parser.add_argument('--operation-id', required=True)
    parser.add_argument('--epoch', required=True)
    parser.add_argument('--manifest-digest', required=True)
    try:
        args = parser.parse_args()
        if not args.operation_id or not args.epoch or not args.manifest_digest:
            raise ValueError('Missing launch binding')
        # Only this packaged app directory is added explicitly. -I excludes
        # cwd, PYTHONPATH and user site; Go owns the verified absolute path.
        sys.path.insert(0, str(Path(__file__).resolve().parent))
        logging.basicConfig(handlers=[Diagnostics()], level=logging.WARNING, force=True)
        logging.captureWarnings(True)
        import pydantic_ai
        pydantic_ai.BANNER_ENABLED = False
        from nocx_agent_runtime.protocol import Wire
        from nocx_agent_runtime.worker import Peer

        async def run():
            wire = await Wire.pipes(sys.stdin.buffer, sys.stdout.buffer, args.operation_id, args.epoch)
            return await Peer(wire, args.manifest_digest).run()

        result = asyncio.run(run())
        if result:
            sys.stderr.write('{"kind":"runtime_error","code":"protocol"}\n')
        return result
    except BaseException:
        # Diagnostics deliberately contain no raw SDK exceptions, provider bodies,
        # user input, tool bytes or environment. Go consumes bounded JSON lines.
        sys.stderr.write(json.dumps({'kind': 'runtime_error', 'code': 'worker'}) + '\n')
        return 2


if __name__ == '__main__':
    raise SystemExit(main())
