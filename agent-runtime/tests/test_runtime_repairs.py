"""Review regressions: real native SDK, observable send gates, no timing sleeps."""
import asyncio
import base64
import json
import struct
from types import SimpleNamespace
import unittest

import httpx2
from pydantic_ai.exceptions import UnexpectedModelBehavior
from pydantic_ai.messages import ModelResponse, ToolCallPart

from nocx_agent_runtime.model import ChatModel
from nocx_agent_runtime.protocol import OBJECT_LIMIT, ProtocolError, Wire, encode, _WriteProtocol
from nocx_agent_runtime.transport import GoIPCTransport
from nocx_agent_runtime.worker import BridgeCalls, Peer
from test_sdk import drive, fixture


class Output:
    def __init__(self, reader):
        self.reader = reader
        self.kind = None

    def write(self, data):
        self.kind = json.loads(data[4:])['kind']
        self.reader.feed_data(data)

    def close(self):
        self.reader.feed_eof()


class FragmentGate(_WriteProtocol):
    def __init__(self):
        super().__init__()
        self.output = None
        self.armed = False
        self.entered = asyncio.Event()
        self.release = asyncio.Event()

    async def drain(self):
        if self.armed and self.output.kind == 'object.chunk' and not self.entered.is_set():
            self.entered.set()
            await self.release.wait()
        await super().drain()


class ObservedWire(Wire):
    def __init__(self, *args):
        super().__init__(*args)
        self.attempts = asyncio.Queue()

    async def _frame(self, kind, request, payload):
        self.attempts.put_nowait(kind)
        await super()._frame(kind, request, payload)


class ObservedPeer(Peer):
    def __init__(self, *args):
        super().__init__(*args)
        self.cancel_observed = asyncio.Event()

    def _cancel(self, owner):
        super()._cancel(owner)
        self.cancel_observed.set()


def wire_pair():
    python_reader, host_reader = asyncio.StreamReader(), asyncio.StreamReader()
    output = Output(host_reader)
    gate = FragmentGate()
    gate.output = output
    python = ObservedWire(python_reader, output, gate, 'operation', 'epoch')
    host = Wire(host_reader, Output(python_reader), _WriteProtocol(), 'operation', 'epoch')
    return python, host, gate


async def next_kind(wire, kind):
    while await wire.attempts.get() != kind:
        pass


def answer_bytes():
    chunk = {'id': 'response', 'object': 'chat.completion.chunk', 'created': 1, 'model': 'fixture',
             'choices': [{'index': 0, 'delta': {'role': 'assistant', 'content': 'sibling-answer'},
                          'finish_reason': 'stop'}]}
    return ('data: ' + json.dumps(chunk) + '\n\ndata: [DONE]\n\n').encode()


class CancellationRepairs(unittest.IsolatedAsyncioTestCase):
    async def _cancel_generation(self, fragmented):
        python, host, gate = wire_pair()
        peer = ObservedPeer(python, 'digest')
        runner = asyncio.create_task(peer.run())
        try:
            await host.send('hello', 'g:1', {'manifestDigest': 'digest', 'bridgeVersion': 1})
            self.assertEqual((await host.receive())[:2], ('hello', 'g:1'))
            if fragmented:
                gate.armed = True
            else:
                await python.write_lock.acquire()
            prompt = 'x' * (1024 * 1024) if fragmented else 'cancel this generation'
            await host.send('generate', 'g:2', {'model': 'fixture', 'messages': [{'role': 'user', 'content': prompt}]})
            if fragmented:
                await asyncio.wait_for(gate.entered.wait(), 5)
            else:
                await asyncio.wait_for(next_kind(python, 'model.open'), 5)
            await host.send('cancel', None, {'ownerRequestId': 'g:2'})
            await asyncio.wait_for(peer.cancel_observed.wait(), 5)
            if fragmented:
                gate.release.set()
            else:
                python.write_lock.release()
            await host.send('generate', 'g:3', {'model': 'fixture', 'messages': [{'role': 'user', 'content': 'sibling'}]})
            opened, closed, outcomes = set(), set(), {}
            while len(outcomes) < 2:
                kind, identity, payload = await asyncio.wait_for(host.receive(), 5)
                if kind == 'model.open':
                    opened.add(identity)
                    await host.send('model.headers', identity, {'status': 200, 'headers': [
                        {'name': 'content-type', 'value': 'text/event-stream'}]})
                    await host.send('model.chunk', None, {'streamId': identity, 'offset': 0,
                                                        'data': base64.b64encode(answer_bytes()).decode()})
                    await host.send('model.end', None, {'streamId': identity, 'error': None})
                elif kind == 'model.close':
                    stream = payload['streamId']
                    self.assertIn(stream, opened, 'Cancellation closed a stream never opened at the host')
                    self.assertNotIn(stream, closed)
                    closed.add(stream)
                elif kind == 'model.chunk':
                    self.assertIn(payload['streamId'], opened)
                elif kind in ('generated', 'error'):
                    self.assertNotIn(identity, outcomes)
                    outcomes[identity] = (kind, payload)
                else:
                    self.fail(f'Unexpected host frame: {kind}')
            self.assertEqual(outcomes['g:2'], ('error', {'code': 'cancelled', 'status': None}))
            self.assertEqual(outcomes['g:3'], ('generated', {'text': 'sibling-answer'}))
            self.assertFalse(peer.failed)
        finally:
            gate.release.set()
            if python.write_lock.locked():
                python.write_lock.release()
            host.close()
            await asyncio.wait_for(runner, 5)

    async def test_cancel_queued_model_open_preserves_sibling_generation(self):
        await self._cancel_generation(False)

    async def test_cancel_fragmented_model_open_preserves_sibling_generation(self):
        await self._cancel_generation(True)

    async def test_cancel_fragmented_send_does_not_poison_next_object(self):
        python, host, gate = wire_pair()
        gate.armed = True
        payload = {'text': 'x' * (1024 * 1024)}
        first = asyncio.create_task(python.send('generated', 'g:2', payload))
        await asyncio.wait_for(gate.entered.wait(), 5)
        first.cancel()
        gate.release.set()
        with self.assertRaises(asyncio.CancelledError):
            await first
        await python.send('generated', 'g:3', payload)
        self.assertEqual(await host.receive(), ('generated', 'g:2', payload))
        self.assertEqual(await host.receive(), ('generated', 'g:3', payload))


class BodyAndRetentionRepairs(unittest.IsolatedAsyncioTestCase):
    async def test_decoded_four_mib_model_body_is_transportable(self):
        python, host, gate = wire_pair()
        peer = Peer(python, 'digest')
        transport = GoIPCTransport(peer, 'g:2')
        content = {'model': 'fixture', 'messages': [{'role': 'user', 'content': ''}], 'stream': True}
        content['messages'][0]['content'] = 'x' * (OBJECT_LIMIT - len(encode(content)))
        body = encode(content)
        self.assertEqual(len(body), OBJECT_LIMIT)
        request = httpx2.Request('POST', 'https://nocx.invalid/v1/chat/completions', content=body)

        async def respond():
            kind, identity, payload = await host.receive()
            self.assertEqual(kind, 'model.open')
            self.assertEqual(base64.b64decode(payload['body']), body)
            peer._reply('model.headers', identity, {'status': 200, 'headers': []})

        response_task = asyncio.create_task(respond())
        request_task = asyncio.create_task(transport.handle_async_request(request))
        try:
            response, _ = await asyncio.wait_for(asyncio.gather(request_task, response_task), 5)
            await response.aclose()
        finally:
            for task in (response_task, request_task):
                if not task.done():
                    task.cancel()
            await asyncio.gather(response_task, request_task, return_exceptions=True)

    async def test_completed_batch_payloads_are_released_but_reuse_stays_rejected(self):
        class Host:
            async def request(self, *args, **kwargs):
                return {'text': 'result', 'kind': 'tool_output'}

        calls = BridgeCalls(Host(), 'g:2')
        for ordinal in range(2):
            response = ModelResponse(parts=[ToolCallPart(tool_name='first', tool_call_id=f'{ordinal}-{index}', args='{}')
                                            for index in range(2)], finish_reason='tool_call')
            await calls.after_model_request(None, request_context=None, response=response)
            for call in response.parts:
                ctx = SimpleNamespace(tool_name=call.tool_name, tool_call_id=call.tool_call_id)
                await calls.invoke(ctx, calls.take(ctx, call.tool_name))
            self.assertEqual(calls.proposals, {})
            self.assertEqual(calls._calls, {})
        with self.assertRaisesRegex(RuntimeError, 'Reused tool call identity'):
            await calls.after_model_request(None, request_context=None, response=response)


class ProviderIdentityRepairs(unittest.IsolatedAsyncioTestCase):
    async def test_missing_original_id_aborts_before_first_sibling_effect(self):
        async with fixture([[('first', 'known-original', '{}'), ('second', None, '{}')]], model_cls=ChatModel) as (
            agent, peer, effects, *_
        ):
            with self.assertRaisesRegex(UnexpectedModelBehavior, 'Provider omitted a final tool call identity'):
                await drive(agent)
            self.assertEqual(effects, [])
            self.assertEqual(len(peer.requests), 1)

    async def test_tool_free_generation_also_rejects_missing_provider_identity(self):
        async with fixture([[('second', None, '{}')]], model_cls=ChatModel) as (agent, server, effects, *_):
            peer = Peer(None, 'digest')
            payload = {'model': 'fixture', 'messages': [{'role': 'user', 'content': 'generate'}]}
            with self.assertRaisesRegex(UnexpectedModelBehavior, 'Provider omitted a final tool call identity'):
                await peer._generate(payload, agent.model)
            self.assertEqual(await peer._generate(payload, agent.model), 'done')
            self.assertEqual(effects, [])
            self.assertEqual(len(server.requests), 2)

    async def test_changed_explicit_streamed_id_aborts_before_effects(self):
        deltas = [
            {'tool_calls': [{'index': 0, 'id': 'original', 'type': 'function',
                             'function': {'name': 'first', 'arguments': '{'}}]},
            {'tool_calls': [{'index': 0, 'id': 'changed', 'function': {'arguments': '}'}}]},
        ]
        async with fixture([[('first', 'original', '{}')]], model_cls=ChatModel, streamed_deltas=deltas) as (
            agent, peer, effects, *_
        ):
            with self.assertRaisesRegex(UnexpectedModelBehavior, 'Provider changed a tool call identity'):
                await drive(agent)
            self.assertEqual(effects, [])

    async def test_later_explicit_streamed_id_is_preserved(self):
        deltas = [
            {'role': 'assistant', 'tool_calls': [{'index': 0, 'type': 'function',
                                                'function': {'name': 'first', 'arguments': '{'}}]},
            {'tool_calls': [{'index': 0, 'id': 'late-original', 'function': {'arguments': '}'}}]},
        ]
        async with fixture([[('first', None, '{}')]], model_cls=ChatModel, streamed_deltas=deltas) as (
            agent, peer, effects, *_
        ):
            result = await drive(agent)
            self.assertEqual(result.output, 'done')
            self.assertEqual(effects, [('first-enter', '{}'), ('first-effect',)])
            tool_messages = [message for message in peer.requests[1]['messages'] if message['role'] == 'tool']
            self.assertEqual([message['tool_call_id'] for message in tool_messages], ['late-original'])


if __name__ == '__main__':
    unittest.main()
