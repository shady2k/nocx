"""Real factory + native OpenAI SDK + local HTTP/SSE peer, no SDK mocks."""
import asyncio
from contextlib import asynccontextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import inspect
import json
import threading
import unittest

import httpx2
from openai import AsyncOpenAI
from pydantic_ai import AgentRunResultEvent, Tool
from pydantic_ai import exceptions as exc
from pydantic_ai.messages import ModelResponse, RetryPromptPart, ToolReturnPart
from pydantic_ai.models.openai import OpenAIChatModel
from pydantic_ai.profiles.openai import OpenAIModelProfile
from pydantic_ai.providers.openai import OpenAIProvider
from pydantic_ai.tool_manager import ToolManager, UnknownToolHandlerError
from pydantic_ai.toolsets import FunctionToolset
from pydantic_deep import DeepAgentDeps, create_deep_agent

from nocx_agent_runtime.sdk import HostCalls, create_host_agent


class ScriptedPeer:
    def __init__(self, batches, streamed_deltas=None):
        self.batches = batches
        self.streamed_deltas = streamed_deltas
        self.requests = []
        peer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                peer.requests.append(body)
                index = len(peer.requests) - 1
                if index < len(peer.batches):
                    calls = peer.batches[index]
                    delta = {'role': 'assistant', 'tool_calls': [
                        {'index': i, 'id': call_id, 'type': 'function',
                         'function': {'name': name, 'arguments': args}}
                        for i, (name, call_id, args) in enumerate(calls)
                    ]}
                    reason = 'tool_calls'
                else:
                    delta = {'role': 'assistant', 'content': 'done', 'reasoning_content': 'reasoning'}
                    reason = 'stop'
                deltas = peer.streamed_deltas if index == 0 and peer.streamed_deltas is not None else [delta]
                chunks = [
                    {'id': 'fixture', 'object': 'chat.completion.chunk', 'created': 1,
                     'model': 'nocx-scripted', 'choices': [{'index': 0, 'delta': value, 'finish_reason': None}]}
                    for value in deltas
                ]
                chunks.append(
                    {'id': 'fixture', 'object': 'chat.completion.chunk', 'created': 1,
                     'model': 'nocx-scripted', 'choices': [{'index': 0, 'delta': {}, 'finish_reason': reason}]},
                )
                data = (''.join('data: ' + json.dumps(chunk) + '\n\n' for chunk in chunks)
                        + 'data: [DONE]\n\n').encode()
                self.send_response(200)
                self.send_header('Content-Type', 'text/event-stream')
                self.send_header('Content-Length', str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        self.server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    async def close(self):
        await asyncio.to_thread(self.server.shutdown)
        self.server.server_close()
        await asyncio.to_thread(self.thread.join)


@asynccontextmanager
async def fixture(batches, *, absent_handler=False, failure=None, parked=False, visible_tools=None,
                  model_cls=OpenAIChatModel, streamed_deltas=None):
    peer = ScriptedPeer(batches, streamed_deltas)
    effects = []
    entered = asyncio.Event()
    release = asyncio.get_running_loop().create_future()

    async def unknown(ctx, call):
        effects.append(('unknown', call.tool_name, call.tool_call_id, call.args))
        if failure is not None:
            raise failure
        return 'host-refusal: unknown or withheld tool'

    capture = HostCalls(unknown, visible_tools=visible_tools)

    async def first(ctx):
        call = capture.take(ctx, 'first') if not absent_handler else None
        effects.append(('first-enter', call.args if call else None))
        entered.set()
        if parked:
            await release
        effects.append(('first-effect',))
        return 'first-result'

    async def second(ctx):
        call = capture.take(ctx, 'second') if not absent_handler else None
        effects.append(('second-effect', call.args if call else None))
        return 'host-malformed-refusal' if call and call.args == '{bad' else 'second-result'

    toolset = FunctionToolset([
        Tool(first, takes_ctx=True, sequential=True),
        Tool(second, takes_ctx=True, sequential=True),
    ])
    async with httpx2.AsyncClient(trust_env=False, follow_redirects=False) as client:
        sdk = AsyncOpenAI(api_key='fixture-only', base_url=f'http://127.0.0.1:{peer.server.server_port}/v1/',
                          http_client=client, max_retries=0)
        model = model_cls('nocx-scripted', provider=OpenAIProvider(openai_client=sdk),
                                profile=OpenAIModelProfile())
        if absent_handler:
            flags = {name: False for name in inspect.signature(create_deep_agent).parameters
                     if name.startswith('include_')}
            agent = create_deep_agent(
                model, toolsets=[toolset], workspace=False, output_type=str, retries=0,
                context_manager=False, eviction_token_limit=None, patch_tool_calls=False,
                stuck_loop_detection=False, cost_tracking=False, thinking=False, forking=False,
                tool_search=False, web_search=False, web_fetch=False, **flags,
            )
            agent.instrument = False
        else:
            agent = create_host_agent(model, instructions='Follow the script.', toolset=toolset, calls=capture)
        try:
            yield agent, peer, effects, entered, release
        finally:
            await peer.close()


async def drive(agent):
    with ToolManager.parallel_execution_mode('sequential'):
        async with agent.run_stream_events('run the script', deps=DeepAgentDeps()) as events:
            async for event in events:
                if isinstance(event, AgentRunResultEvent):
                    return event.result
    raise AssertionError('No result event')


class SDKTests(unittest.IsolatedAsyncioTestCase):
    async def test_no_handler_preserves_upfront_upstream_failure(self):
        async with fixture([[('first', 'known', '{}'), ('absent', 'unknown', '{bad')]],
                           absent_handler=True) as (agent, peer, effects, *_):
            with self.assertRaises(exc.UnexpectedModelBehavior) as failure:
                await drive(agent)
            self.assertIn('Unknown tool name', str(failure.exception.__cause__))
            self.assertEqual(effects, [])
            self.assertEqual(len(peer.requests), 1)

    async def test_known_then_unknown_preserves_ids_and_raw_args(self):
        async with fixture([[('first', 'known-original', '{}'), ('absent', 'unknown-original', '{bad')]]) as (
            agent, peer, effects, *_
        ):
            result = await drive(agent)
            self.assertEqual(result.output, 'done')
            self.assertEqual(effects, [('first-enter', '{}'), ('first-effect',),
                                       ('unknown', 'absent', 'unknown-original', '{bad')])
            returns = [p for m in result.all_messages() for p in m.parts if isinstance(p, ToolReturnPart)]
            self.assertEqual([(p.tool_name, p.tool_call_id) for p in returns],
                             [('first', 'known-original'), ('absent', 'unknown-original')])
            self.assertEqual(returns[1].content, 'host-refusal: unknown or withheld tool')
            sent = [m for m in peer.requests[1]['messages'] if m['role'] == 'tool']
            self.assertEqual([m['tool_call_id'] for m in sent], ['known-original', 'unknown-original'])
            self.assertEqual(sent[1]['content'], 'host-refusal: unknown or withheld tool')
            for request in peer.requests:
                self.assertEqual({t['function']['name'] for t in request['tools']}, {'first', 'second'})
                self.assertTrue(request['stream'])
                self.assertEqual(request['model'], 'nocx-scripted')

    async def test_handler_survives_next_run_step(self):
        async with fixture([[('first', 'one', '{}')], [('absent', 'two', '{bad')]]) as (
            agent, peer, effects, *_
        ):
            result = await drive(agent)
            self.assertEqual(result.output, 'done')
            self.assertEqual(len(peer.requests), 3)
            self.assertEqual(effects[-1], ('unknown', 'absent', 'two', '{bad'))

    async def test_unknown_is_parked_behind_known_approval(self):
        async with fixture([[('first', 'one', '{}'), ('absent', 'two', '{}')]], parked=True) as (
            agent, peer, effects, entered, release
        ):
            task = asyncio.create_task(drive(agent))
            try:
                await asyncio.wait_for(entered.wait(), 5)
                self.assertFalse(task.done())
                self.assertEqual(effects, [('first-enter', '{}')])
                self.assertEqual(len(peer.requests), 1)
                release.set_result('approved')
                result = await asyncio.wait_for(task, 5)
                self.assertEqual(result.output, 'done')
                self.assertEqual(effects, [('first-enter', '{}'), ('first-effect',),
                                           ('unknown', 'absent', 'two', '{}')])
            finally:
                if not task.done():
                    task.cancel()
                    await asyncio.gather(task, return_exceptions=True)

    async def test_cancel_pending_approval_never_runs_unknown(self):
        async with fixture([[('first', 'one', '{}'), ('absent', 'two', '{}')]], parked=True) as (
            agent, peer, effects, entered, release
        ):
            task = asyncio.create_task(drive(agent))
            try:
                await asyncio.wait_for(entered.wait(), 5)
                task.cancel()
                with self.assertRaises(asyncio.CancelledError):
                    await task
                self.assertTrue(release.cancelled())
                self.assertEqual(effects, [('first-enter', '{}')])
                self.assertEqual(len(peer.requests), 1)
            finally:
                if not task.done():
                    task.cancel()
                    await asyncio.gather(task, return_exceptions=True)

    async def test_malformed_known_call_keeps_earlier_effect(self):
        async with fixture([[('first', 'one', '{}'), ('second', 'two', '{bad')]]) as (
            agent, peer, effects, *_
        ):
            result = await drive(agent)
            self.assertEqual(result.output, 'done')
            self.assertEqual(effects, [('first-enter', '{}'), ('first-effect',), ('second-effect', '{bad')])
            self.assertEqual(len(peer.requests), 2)

    async def test_duplicate_ids_rejected_before_any_effect(self):
        async with fixture([[('first', 'duplicate', '{}'), ('absent', 'duplicate', '{}')]]) as (
            agent, peer, effects, *_
        ):
            with self.assertRaisesRegex(RuntimeError, 'duplicate tool call identity'):
                await drive(agent)
            self.assertEqual(effects, [])
            self.assertEqual(len(peer.requests), 1)

    async def test_reused_identity_never_replays_effect(self):
        async with fixture([[('first', 'same', '{}')], [('first', 'same', '{}')]]) as (
            agent, peer, effects, *_
        ):
            with self.assertRaisesRegex(RuntimeError, 'Reused tool call identity'):
                await drive(agent)
            self.assertEqual(effects, [('first-enter', '{}'), ('first-effect',)])
            self.assertEqual(len(peer.requests), 2)

    async def test_hidden_known_tool_executes_and_visibility_refreshes(self):
        projections = []

        async def visible(ctx):
            # First request hides second; its guessed name still reaches the host.
            names = {'first'} if not projections else {'first', 'second'}
            projections.append(names)
            return names

        async with fixture([[('second', 'hidden-known', '{}')]], visible_tools=visible) as (
            agent, peer, effects, *_
        ):
            result = await drive(agent)
            self.assertEqual(result.output, 'done')
            self.assertEqual(effects, [('second-effect', '{}')])
            self.assertEqual(projections, [{'first'}, {'first', 'second'}])
            self.assertEqual(
                [{tool['function']['name'] for tool in request['tools']} for request in peer.requests],
                [{'first'}, {'first', 'second'}],
            )


    async def test_handler_failure_aborts_without_retry_or_later_effect(self):
        failures = [
            RuntimeError('host failed'), exc.ModelRetry('retry'), exc.ToolFailed('failed'),
            exc.CallDeferred(), exc.ApprovalRequired(), exc.SkipToolValidation({}),
            exc.SkipToolExecution('skip'), exc.SkipModelRequest(ModelResponse(parts=[])),
            exc.ToolRetryError(RetryPromptPart(content='retry')),
            exc.ToolFailedError(ToolReturnPart(tool_name='absent', content='failed')),
            exc.RunCancelled('nested cancellation'), exc.UnexpectedModelBehavior('bad'),
            ExceptionGroup('group', [exc.ModelRetry('nested retry')]),
        ]
        for failure in failures:
            with self.subTest(exception=type(failure).__name__):
                async with fixture([[('absent', 'one', '{bad'), ('second', 'two', '{}')]], failure=failure) as (
                    agent, peer, effects, *_
                ):
                    with self.assertRaises(UnknownToolHandlerError) as caught:
                        await drive(agent)
                    self.assertIs(caught.exception.__cause__, failure)
                    self.assertEqual(effects, [('unknown', 'absent', 'one', '{bad')])
                    self.assertEqual(len(peer.requests), 1)


if __name__ == '__main__':
    unittest.main()
