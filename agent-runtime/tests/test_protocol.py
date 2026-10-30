"""Bounds and lifecycle checks at the private protocol boundary."""
import asyncio
import base64
import hashlib
import json
import struct
import unittest

import httpx2

from nocx_agent_runtime.protocol import (
    CHUNK_LIMIT, FRAME_LIMIT, OBJECT_LIMIT, STREAM_CREDIT, ProtocolError,
    Wire, decode, encode, _WriteProtocol,
)
from nocx_agent_runtime.transport import GoIPCTransport, ResponseStream
from nocx_agent_runtime.worker import Peer


class MemoryOutput:
    def __init__(self):
        self.data = bytearray()
        self.closed = False

    def write(self, value):
        self.data.extend(value)

    def close(self):
        self.closed = True


class RecordingPeer:
    closing = False

    def __init__(self):
        self.notifications = []

    async def notify(self, kind, payload):
        self.notifications.append((kind, payload))


class RecordingWire:
    def __init__(self):
        self.sent = []

    async def send(self, kind, identity, payload):
        self.sent.append((kind, identity, payload))


def wire():
    reader = asyncio.StreamReader()
    output = MemoryOutput()
    return Wire(reader, output, _WriteProtocol(), 'operation', 'epoch')


def incoming(target, kind, request, payload, *, seq=None, **overrides):
    item = target.envelope(kind, request, payload, seq if seq is not None else target.in_seq + 1)
    item.update(overrides)
    data = encode(item)
    target.reader.feed_data(struct.pack('>I', len(data)) + data)


class WireTests(unittest.IsolatedAsyncioTestCase):
    async def test_large_object_roundtrip_at_bound(self):
        sender = wire()
        # Exact payload limit includes JSON syntax, not just the string value.
        payload = {'text': 'x' * (OBJECT_LIMIT - len(encode({'text': ''})))}
        await sender.send('generated', 'g:2', payload)
        receiver = wire()
        receiver.reader.feed_data(sender.transport.data)
        kind, identity, actual = await receiver.receive()
        self.assertEqual((kind, identity, actual), ('generated', 'g:2', payload))
        self.assertIsNone(receiver.in_object)

    async def test_oversized_logical_object_never_sends(self):
        target = wire()
        with self.assertRaises(ProtocolError):
            await target.send('generated', 'g:2', {'text': 'x' * OBJECT_LIMIT})
        self.assertEqual(target.transport.data, b'')

    async def test_frame_bound_checked_before_body_read(self):
        target = wire()
        target.reader.feed_data(struct.pack('>I', FRAME_LIMIT + 1))
        with self.assertRaises(ProtocolError):
            await target.receive()

    async def test_wrong_binding_sequence_version_and_unknown_fields(self):
        cases = [
            {'operationId': 'different'}, {'epoch': 'old'}, {'seq': 2},
            {'version': True}, {'version': 2}, {'unexpected': 1},
        ]
        for overrides in cases:
            with self.subTest(overrides=overrides):
                target = wire()
                incoming(target, 'hello', 'g:1', {}, **overrides)
                with self.assertRaises(ProtocolError):
                    await target.receive()

    async def test_duplicate_json_keys_rejected(self):
        for value in (b'{"kind":1,"kind":2}', b'{"nested":{"id":1,"id":2}}', b'{"value":NaN}'):
            with self.subTest(value=value), self.assertRaises(ProtocolError):
                decode(value)

    async def test_objects_require_consistent_digest_ordinal_and_commit(self):
        data = encode({'text': 'result'})
        common = {'objectId': 'gobj:1', 'kind': 'generated', 'requestId': 'g:2', 'totalBytes': len(data)}
        for mode in ('digest', 'ordinal', 'metadata', 'eof', 'reused'):
            with self.subTest(mode=mode):
                target = wire()
                chunk = dict(common, ordinal=1 if mode == 'ordinal' else 0,
                             data=base64.b64encode(data).decode())
                incoming(target, 'object.chunk', None, chunk, seq=1)
                if mode == 'eof':
                    target.reader.feed_eof()
                else:
                    commit = dict(common, chunks=1, sha256=hashlib.sha256(data).hexdigest())
                    if mode == 'digest':
                        commit['sha256'] = '0' * 64
                    elif mode == 'metadata':
                        commit['kind'] = 'complete'
                    incoming(target, 'object.commit', None, commit, seq=2)
                if mode == 'reused':
                    self.assertEqual((await target.receive())[2], {'text': 'result'})
                    incoming(target, 'object.chunk', None, chunk, seq=3)
                with self.assertRaises(ProtocolError):
                    await target.receive()

    async def test_truncated_frame_rejected(self):
        target = wire()
        target.reader.feed_data(struct.pack('>I', 5) + b'{}')
        target.reader.feed_eof()
        with self.assertRaises(ProtocolError):
            await target.receive()

    async def test_valid_unknown_reply_and_repeated_request_fail_closed(self):
        peer = Peer(RecordingWire(), 'digest')
        with self.assertRaises(ProtocolError):
            peer._reply('tool.result', 'p:99', {'text': 'x', 'kind': 'nocx_message'})
        peer._register('g:1')
        with self.assertRaises(ProtocolError):
            peer._register('g:1')
        with self.assertRaises(ProtocolError):
            peer._register('p:1')

    async def test_cancel_before_generation_task_starts_settles_once(self):
        peer = Peer(RecordingWire(), 'digest')
        await peer._receive('generate', 'g:1', {'model': 'fixture', 'messages': [{'role': 'user', 'content': 'hi'}]})
        task = peer.tasks['g:1']
        await peer._receive('cancel', None, {'ownerRequestId': 'g:1'})
        await task
        self.assertEqual(peer.wire.sent, [('error', 'g:1', {'code': 'cancelled', 'status': None})])
        await peer._receive('cancel', None, {'ownerRequestId': 'g:1'})
        self.assertEqual(len(peer.wire.sent), 1)

    async def test_event_splitting_preserves_unicode_and_order(self):
        peer = Peer(RecordingWire(), 'digest')
        value = 'word\U0001f600' * CHUNK_LIMIT
        await peer._events('g:2', 'answer', value)
        events = [payload for kind, identity, payload in peer.wire.sent]
        self.assertEqual(''.join(item['text'] for item in events), value)
        self.assertTrue(all(len(item['text'].encode()) <= CHUNK_LIMIT for item in events))
        self.assertTrue(all(item['ownerRequestId'] == 'g:2' and item['kind'] == 'answer' for item in events))

    async def test_http_transport_refuses_real_endpoints(self):
        transport = GoIPCTransport(RecordingPeer(), 'g:2')
        for method, url in [('GET', 'https://nocx.invalid/v1/chat/completions'),
                            ('POST', 'https://example.com/v1/chat/completions'),
                            ('POST', 'https://nocx.invalid/v1/models')]:
            with self.subTest(method=method, url=url), self.assertRaises(ProtocolError):
                await transport.handle_async_request(httpx2.Request(method, url))


class StreamTests(unittest.IsolatedAsyncioTestCase):
    async def test_credit_is_acknowledged_only_after_consumption(self):
        peer = RecordingPeer()
        stream = ResponseStream(peer, 'p:1', 'g:2')
        stream.accept_headers({'status': 200, 'headers': []})
        stream.chunk({'streamId': 'p:1', 'data': base64.b64encode(b'abc').decode(), 'offset': 0})
        stream.end({'streamId': 'p:1', 'error': None})
        iterator = stream.__aiter__()
        self.assertEqual(await anext(iterator), b'abc')
        self.assertEqual(peer.notifications, [])
        with self.assertRaises(StopAsyncIteration):
            await anext(iterator)
        self.assertEqual(peer.notifications, [('model.chunk', {'streamId': 'p:1', 'ackBytes': 3})])
        await stream.aclose()
        await stream.aclose()
        self.assertEqual(peer.notifications[-1], ('model.close', {'streamId': 'p:1'}))
        self.assertEqual(len(peer.notifications), 2)

    async def test_stream_credit_overflow_and_wrong_offsets_fail(self):
        for mode in ('credit', 'offset', 'before_headers', 'after_end'):
            with self.subTest(mode=mode):
                stream = ResponseStream(RecordingPeer(), 'p:1', 'g:2')
                if mode != 'before_headers':
                    stream.accept_headers({'status': 200, 'headers': []})
                if mode == 'after_end':
                    stream.end({'streamId': 'p:1', 'error': None})
                if mode == 'credit':
                    for offset in range(0, STREAM_CREDIT, CHUNK_LIMIT):
                        stream.chunk({'streamId': 'p:1', 'data': base64.b64encode(b'x' * CHUNK_LIMIT).decode(),
                                      'offset': offset})
                with self.assertRaises(ProtocolError):
                    stream.chunk({'streamId': 'p:1', 'data': 'eA==',
                                  'offset': 99 if mode == 'offset' else stream.offset})

    async def test_close_racing_inflight_headers_and_chunks_uses_tombstone(self):
        peer = RecordingPeer()
        stream = ResponseStream(peer, 'p:1', 'g:2')
        stream.opened = True  # The model.open envelope committed before cancellation.
        await stream.aclose()
        stream.accept_headers({'status': 200, 'headers': []})
        stream.chunk({'streamId': 'p:1', 'data': 'eA==', 'offset': 0})
        stream.end({'streamId': 'p:1', 'error': None})
        self.assertTrue(stream.queue.empty())
        self.assertEqual(peer.notifications, [('model.close', {'streamId': 'p:1'})])
        with self.assertRaises(ProtocolError):
            stream.end({'streamId': 'p:1', 'error': None})
        with self.assertRaises(ProtocolError):
            stream.accept_headers({'status': 200, 'headers': []})

    async def test_host_stream_error_is_not_eof(self):
        stream = ResponseStream(RecordingPeer(), 'p:1', 'g:2')
        stream.accept_headers({'status': 200, 'headers': []})
        stream.end({'streamId': 'p:1', 'error': 'body_limit'})
        with self.assertRaises(httpx2.ReadError):
            await anext(stream.__aiter__())


if __name__ == '__main__':
    unittest.main()
