"""httpx2 transport over private IPC; never resolves or dials an endpoint."""
import asyncio
import base64

import httpx2

from .protocol import CHUNK_LIMIT, OBJECT_LIMIT, STREAM_CREDIT, ProtocolError, fields, headers, number, text, unbase64

SYNTHETIC_URL = 'https://nocx.invalid/v1/chat/completions'
_EOF = object()


class ResponseStream(httpx2.AsyncByteStream):
    def __init__(self, peer, stream_id, owner):
        self.peer = peer
        self.stream_id = stream_id
        self.owner = owner
        self.queue = asyncio.Queue()
        self.offset = 0
        self.buffered = 0
        self.ended = False
        self.closed = False
        self.headers_received = False
        self.opened = False

    def accept_headers(self, payload):
        if self.headers_received:
            raise ProtocolError('Repeated model headers')
        fields(payload, {'status', 'headers'})
        number(payload['status'], 100, 599)
        result = headers(payload['headers'])
        self.headers_received = True
        self.opened = True
        return payload['status'], result

    def chunk(self, payload):
        fields(payload, {'streamId', 'data', 'offset'})
        if not self.headers_received or self.ended or number(payload['offset']) != self.offset:
            raise ProtocolError('Out-of-order HTTP chunk')
        data = unbase64(payload['data'], CHUNK_LIMIT)
        if not data:
            raise ProtocolError('Empty HTTP chunk')
        self.offset += len(data)
        if self.closed:
            # close crosses already-written frames on independent pipe directions.
            # The identity/offset are still validated; Go releases credit on close.
            return
        if self.buffered + len(data) > STREAM_CREDIT:
            raise ProtocolError('HTTP stream exceeded credit')
        self.buffered += len(data)
        self.queue.put_nowait(data)

    def end(self, payload):
        fields(payload, {'streamId', 'error'})
        if not self.headers_received or self.ended or payload['error'] not in (None, 'transport', 'body_limit'):
            raise ProtocolError('Invalid HTTP stream end')
        self.ended = True
        if not self.closed:
            item = _EOF if payload['error'] is None else httpx2.ReadError('Host HTTP stream failed')
            self.queue.put_nowait(item)

    def abort(self):
        if not self.closed:
            self.queue.put_nowait(httpx2.ReadError('Private model transport closed'))

    async def __aiter__(self):
        while not self.closed:
            item = await self.queue.get()
            if item is _EOF:
                return
            if isinstance(item, Exception):
                raise item
            yield item
            if self.closed:
                return
            # Only acknowledge after the consumer has received this entire chunk.
            self.buffered -= len(item)
            await self.peer.notify('model.chunk', {'streamId': self.stream_id, 'ackBytes': len(item)})

    async def aclose(self):
        if self.closed:
            return
        self.closed = True
        while not self.queue.empty():
            self.queue.get_nowait()
        self.buffered = 0
        if self.opened and not self.peer.closing:
            await self.peer.notify('model.close', {'streamId': self.stream_id})


class GoIPCTransport(httpx2.AsyncBaseTransport):
    def __init__(self, peer, owner):
        self.peer = peer
        self.owner = owner

    async def handle_async_request(self, request):
        if request.method != 'POST' or str(request.url) != SYNTHETIC_URL:
            raise ProtocolError('Model attempted an unsupported HTTP route')
        body = await request.aread()
        if len(body) > OBJECT_LIMIT:
            raise ProtocolError('Model request exceeds object bound')
        request_id = self.peer.next_request_id()
        stream = ResponseStream(self.peer, request_id, self.owner)
        self.peer.streams[request_id] = stream
        allowed_headers = [
            {'name': name, 'value': value}
            for name, value in request.headers.multi_items()
            if name.lower() in {'content-type', 'accept', 'user-agent'} or name.lower().startswith('x-stainless-')
        ]
        try:
            payload = await self.peer.request('model.open', 'model.headers', {
                'ownerRequestId': self.owner,
                'method': 'POST', 'url': SYNTHETIC_URL,
                'headers': allowed_headers, 'body': base64.b64encode(body).decode('ascii'),
            }, request_id=request_id)
            # Headers were validated/registered by the reader before it accepted
            # any subsequent stream chunk. No second state transition here.
            return httpx2.Response(payload['status'], headers=headers(payload['headers']), stream=stream)
        except BaseException:
            await stream.aclose()
            if not stream.opened:
                self.peer.streams.pop(request_id, None)
            raise

    async def aclose(self):
        for stream in list(self.peer.streams.values()):
            if stream.owner == self.owner:
                await stream.aclose()
