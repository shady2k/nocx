"""Version-one private pipe framing and bounded logical objects.

Only this module owns wire serialization. It does not parse model HTTP/SSE.
"""
import asyncio
import base64
import hashlib
import json
import re
import struct
from dataclasses import dataclass
from typing import Any

VERSION = 1
FRAME_LIMIT = 1024 * 1024
OBJECT_LIMIT = 4 * 1024 * 1024
MODEL_OPEN_LIMIT = ((OBJECT_LIMIT + 2) // 3) * 4 + 64 * 1024
CHUNK_LIMIT = 32 * 1024
STREAM_CREDIT = 256 * 1024
ARGS_LIMIT = 64 * 1024
MAX_COUNTER = (1 << 64) - 1
ENVELOPE_KEYS = {'version', 'kind', 'operationId', 'epoch', 'seq', 'requestId', 'payload'}
OBJECT_FORBIDDEN = {'object.chunk', 'object.commit', 'model.chunk', 'model.end', 'model.close'}
KINDS = {'hello', 'start', 'catalog.request', 'catalog.result', 'tool.invoke', 'tool.result',
         'model.open', 'model.headers', 'model.chunk', 'model.end', 'model.close', 'generate',
         'generated', 'event', 'complete', 'cancel', 'error', 'object.chunk', 'object.commit'}


def object_limit(kind: str) -> int:
    """model.open carries up to 4 MiB of body plus base64 and bounded metadata."""
    return MODEL_OPEN_LIMIT if kind == 'model.open' else OBJECT_LIMIT


class ProtocolError(RuntimeError):
    """A protocol failure; never expose raw input or exception details."""


class RemoteError(RuntimeError):
    def __init__(self, code: str, status: int | None):
        super().__init__(code)
        self.code = code
        self.status = status


def fields(value: Any, names: set[str]) -> dict:
    if type(value) is not dict or value.keys() != names:
        raise ProtocolError('Unexpected object fields')
    return value


def text(value: Any, *, nonempty: bool = False) -> str:
    if type(value) is not str or (nonempty and not value):
        raise ProtocolError('Expected string')
    # Python's JSON decoder permits escaped unpaired surrogates. UTF-8 wire
    # strings cannot represent them, so reject before identities/bytes are used.
    try:
        value.encode('utf-8')
    except UnicodeError as error:
        raise ProtocolError('Invalid Unicode string') from error
    return value


def number(value: Any, minimum: int = 0, maximum: int = MAX_COUNTER) -> int:
    if type(value) is not int or not minimum <= value <= maximum:
        raise ProtocolError('Invalid integer')
    return value


def request_id(value: Any, prefix: str | None = None) -> str:
    value = text(value, nonempty=True)
    match = re.fullmatch(r'([gp]):([1-9][0-9]*)', value)
    if match is None or (prefix is not None and match[1] != prefix):
        raise ProtocolError('Invalid request identity')
    number(int(match[2]), 1)
    return value


def _pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ProtocolError('Duplicate JSON key')
        result[key] = value
    return result


def _constant(_value):
    raise ProtocolError('Non-finite JSON number')


def decode(data: bytes) -> dict:
    try:
        value = json.loads(data.decode('utf-8'), object_pairs_hook=_pairs, parse_constant=_constant)
    except (ValueError, UnicodeError, RecursionError) as error:
        raise ProtocolError('Invalid JSON') from error
    if type(value) is not dict:
        raise ProtocolError('Expected JSON object')
    return value


def encode(value: dict) -> bytes:
    try:
        return json.dumps(value, ensure_ascii=False, separators=(',', ':'), allow_nan=False).encode('utf-8')
    except (ValueError, TypeError, UnicodeError, RecursionError) as error:
        raise ProtocolError('Invalid output JSON') from error


def unbase64(value: Any, maximum: int = CHUNK_LIMIT) -> bytes:
    value = text(value)
    if len(value) > ((maximum + 2) // 3) * 4:
        raise ProtocolError('Encoded bytes exceed bound')
    try:
        result = base64.b64decode(value, validate=True)
    except ValueError as error:
        raise ProtocolError('Invalid base64') from error
    if len(result) > maximum:
        raise ProtocolError('Decoded bytes exceed bound')
    return result


def headers(value: Any) -> list[tuple[str, str]]:
    if type(value) is not list:
        raise ProtocolError('Expected headers')
    result = []
    for item in value:
        fields(item, {'name', 'value'})
        name, content = text(item['name'], nonempty=True), text(item['value'])
        if not re.fullmatch(r"[!#$%&'*+.^_`|~0-9A-Za-z-]+", name) or '\r' in content or '\n' in content:
            raise ProtocolError('Invalid HTTP header')
        result.append((name, content))
    return result


def messages(value: Any) -> list[dict]:
    if type(value) is not list:
        raise ProtocolError('Expected messages')
    for item in value:
        fields(item, {'role', 'content'})
        if item['role'] not in ('system', 'user', 'assistant'):
            raise ProtocolError('Unsupported message role')
        text(item['content'])
    return value


def catalog(value: dict) -> dict:
    fields(value, {'tools', 'visibleTools'})
    if type(value['tools']) is not list or type(value['visibleTools']) is not list:
        raise ProtocolError('Invalid catalog')
    names = set()
    for tool in value['tools']:
        fields(tool, {'name', 'description', 'parameters'})
        name = text(tool['name'], nonempty=True)
        text(tool['description'])
        if name in names or type(tool['parameters']) is not dict:
            raise ProtocolError('Invalid tool definition')
        names.add(name)
    visible = [text(name, nonempty=True) for name in value['visibleTools']]
    if len(set(visible)) != len(visible) or not set(visible) <= names:
        raise ProtocolError('Invalid visible catalog')
    return value


class _WriteProtocol(asyncio.Protocol):
    def __init__(self):
        self.paused = False
        self.waiter = None
        self.closed = False

    def pause_writing(self):
        self.paused = True

    def resume_writing(self):
        self.paused = False
        if self.waiter is not None and not self.waiter.done():
            self.waiter.set_result(None)

    def connection_lost(self, error):
        self.closed = True
        if self.waiter is not None and not self.waiter.done():
            self.waiter.set_exception(EOFError('Private output closed'))

    async def drain(self):
        if self.closed:
            raise EOFError('Private output closed')
        if self.paused:
            self.waiter = asyncio.get_running_loop().create_future()
            try:
                await self.waiter
            finally:
                self.waiter = None


@dataclass
class _Object:
    object_id: str
    kind: str
    request: str | None
    total: int
    ordinal: int
    data: bytearray


class Wire:
    def __init__(self, reader, transport, write_protocol, operation_id, epoch):
        self.reader = reader
        self.transport = transport
        self.write_protocol = write_protocol
        self.operation_id = text(operation_id, nonempty=True)
        self.epoch = text(epoch, nonempty=True)
        self.in_seq = 0
        self.out_seq = 0
        self.object_seq = 0
        self.in_object = None
        self.seen_objects = set()
        self.write_lock = asyncio.Lock()
        self.object_lock = asyncio.Lock()

    @classmethod
    async def pipes(cls, stdin, stdout, operation_id, epoch):
        loop = asyncio.get_running_loop()
        reader = asyncio.StreamReader(limit=FRAME_LIMIT)
        read_transport, _ = await loop.connect_read_pipe(lambda: asyncio.StreamReaderProtocol(reader), stdin)
        protocol = _WriteProtocol()
        transport, _ = await loop.connect_write_pipe(lambda: protocol, stdout)
        wire = cls(reader, transport, protocol, operation_id, epoch)
        wire.read_transport = read_transport
        return wire

    def envelope(self, kind, request, payload, seq):
        return {'version': VERSION, 'kind': kind, 'operationId': self.operation_id,
                'epoch': self.epoch, 'seq': seq, 'requestId': request, 'payload': payload}

    async def _frame(self, kind, request, payload):
        async with self.write_lock:
            self.out_seq += 1
            number(self.out_seq, 1)
            data = encode(self.envelope(kind, request, payload, self.out_seq))
            if len(data) > FRAME_LIMIT:
                raise ProtocolError('Envelope exceeds bound')
            self.transport.write(struct.pack('>I', len(data)) + data)
            await self.write_protocol.drain()

    async def send(self, kind, request, payload, *, on_commit=None):
        """Finish one logical send before delivering cancellation to its caller.

        A fragment or physical frame may already be visible on the other pipe.
        Draining the committed send preserves framing and lets a cancelled model
        request close only an ID the host has actually received. The host owns
        operation shutdown/deadlines if its reader stops making progress.
        """
        async def commit():
            await self._send(kind, request, payload)
            if on_commit is not None:
                on_commit()

        task = asyncio.create_task(commit())
        cancellation = None
        while True:
            try:
                await asyncio.shield(task)
                break
            except asyncio.CancelledError as error:
                cancellation = error
                if task.done():
                    break
        # A failed partial send is fatal, not a safely completed cancellation.
        task.result()
        if cancellation is not None:
            raise cancellation

    async def _send(self, kind, request, payload):
        # Account for the widest legal sequence so concurrent sends cannot turn
        # an exactly-at-limit logical envelope into an oversized physical frame.
        if len(encode(self.envelope(kind, request, payload, MAX_COUNTER))) <= FRAME_LIMIT:
            await self._frame(kind, request, payload)
            return
        if kind in OBJECT_FORBIDDEN:
            raise ProtocolError('Streaming frame exceeds bound')
        data = encode(payload)
        if len(data) > object_limit(kind):
            raise ProtocolError('Logical object exceeds bound')
        async with self.object_lock:
            self.object_seq += 1
            object_id = f'pobj:{self.object_seq}'
            common = {'objectId': object_id, 'kind': kind, 'requestId': request, 'totalBytes': len(data)}
            chunks = 0
            for offset in range(0, len(data), CHUNK_LIMIT):
                await self._frame('object.chunk', None, dict(
                    common, ordinal=chunks, data=base64.b64encode(data[offset:offset + CHUNK_LIMIT]).decode('ascii')))
                chunks += 1
            await self._frame('object.commit', None, dict(
                common, chunks=chunks, sha256=hashlib.sha256(data).hexdigest()))

    async def receive(self):
        while True:
            try:
                prefix = await self.reader.readexactly(4)
            except asyncio.IncompleteReadError as error:
                if error.partial or self.in_object is not None:
                    raise ProtocolError('Incomplete frame or object') from error
                raise EOFError('Private input closed') from error
            length = struct.unpack('>I', prefix)[0]
            number(length, 1, FRAME_LIMIT)
            try:
                data = await self.reader.readexactly(length)
            except asyncio.IncompleteReadError as error:
                raise ProtocolError('Incomplete frame') from error
            item = fields(decode(data), ENVELOPE_KEYS)
            if type(item['version']) is not int or item['version'] != VERSION:
                raise ProtocolError('Wrong bridge version')
            if item['operationId'] != self.operation_id or item['epoch'] != self.epoch:
                raise ProtocolError('Wrong operation binding')
            if number(item['seq'], 1) != self.in_seq + 1:
                raise ProtocolError('Out-of-order envelope')
            self.in_seq = item['seq']
            kind = text(item['kind'])
            if kind not in KINDS or type(item['payload']) is not dict:
                raise ProtocolError('Invalid envelope kind or payload')
            if item['requestId'] is not None:
                request_id(item['requestId'])
            if kind not in ('object.chunk', 'object.commit'):
                return kind, item['requestId'], item['payload']
            if item['requestId'] is not None:
                raise ProtocolError('Object frames are notifications')
            logical = self._object(kind, item['payload'])
            if logical is not None:
                return logical

    def _object(self, kind, payload):
        keys = {'objectId', 'kind', 'requestId', 'totalBytes'}
        fields(payload, keys | ({'ordinal', 'data'} if kind == 'object.chunk' else {'chunks', 'sha256'}))
        object_id = text(payload['objectId'], nonempty=True)
        logical_kind = text(payload['kind'])
        if logical_kind not in KINDS or logical_kind in OBJECT_FORBIDDEN:
            raise ProtocolError('Invalid logical object kind')
        request = payload['requestId']
        if request is not None:
            request_id(request)
        total = number(payload['totalBytes'], 1, object_limit(logical_kind))
        if kind == 'object.chunk' and self.in_object is None:
            if object_id in self.seen_objects:
                raise ProtocolError('Reused object identity')
            self.seen_objects.add(object_id)
            self.in_object = _Object(object_id, logical_kind, request, total, 0, bytearray())
        state = self.in_object
        if state is None or (object_id, logical_kind, request, total) != (
            state.object_id, state.kind, state.request, state.total
        ):
            raise ProtocolError('Conflicting object identity')
        if kind == 'object.chunk':
            if number(payload['ordinal']) != state.ordinal:
                raise ProtocolError('Out-of-order object chunk')
            data = unbase64(payload['data'])
            if not data or len(state.data) + len(data) > total:
                raise ProtocolError('Object length exceeds declaration')
            state.data.extend(data)
            state.ordinal += 1
            return None
        digest = text(payload['sha256'])
        if (number(payload['chunks'], 1) != state.ordinal or len(state.data) != total
                or not re.fullmatch('[0-9a-f]{64}', digest)
                or hashlib.sha256(state.data).hexdigest() != digest):
            raise ProtocolError('Invalid object commit')
        result = logical_kind, request, decode(state.data)
        self.in_object = None
        return result

    def close(self):
        self.transport.close()
        if hasattr(self, 'read_transport'):
            self.read_transport.close()
