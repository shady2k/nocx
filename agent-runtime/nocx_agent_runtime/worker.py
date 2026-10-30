"""Production child: SDK runs and nested native generations over one private peer."""
import asyncio
import json
from dataclasses import dataclass

from pydantic_ai import AgentRunResultEvent
from pydantic_ai.exceptions import ModelHTTPError
from pydantic_ai.messages import (
    PartDeltaEvent, PartStartEvent, TextPart, TextPartDelta, ThinkingPart,
    ThinkingPartDelta, ToolCallPart,
)
from pydantic_ai.models import ModelRequestParameters
from pydantic_ai.tool_manager import ToolManager
from pydantic_ai.tools import ToolDefinition
from pydantic_ai.toolsets import AbstractToolset
from pydantic_ai.toolsets.abstract import ToolsetTool
from pydantic_ai.usage import UsageLimits
from pydantic_core import SchemaValidator, core_schema
from pydantic_deep import DeepAgentDeps

from .model import history, native_model
from .protocol import (
    ARGS_LIMIT, CHUNK_LIMIT, VERSION, ProtocolError, RemoteError, catalog,
    fields, messages, number, request_id, text,
)
from .sdk import HostCalls, create_host_agent


class EmptyAnswer(RuntimeError):
    """A normally completed model response contained no answer or tool calls."""


@dataclass
class Pending:
    expected: str
    future: asyncio.Future
    sent: bool = False


def safe_failure(error):
    """Return only the declared safe category/status, never provider/host text."""
    seen = set()
    current = error
    while current is not None and id(current) not in seen:
        seen.add(id(current))
        if isinstance(current, EmptyAnswer):
            return {'code': 'empty_answer', 'status': None}
        if isinstance(current, ProtocolError):
            return {'code': 'protocol', 'status': None}
        if isinstance(current, RemoteError):
            return {'code': current.code, 'status': current.status}
        if isinstance(current, ModelHTTPError):
            return {'code': 'provider', 'status': current.status_code}
        current = current.__cause__
    return {'code': 'provider', 'status': None}


class BridgeCalls(HostCalls):
    def __init__(self, peer, owner):
        super().__init__(self.invoke, visible_tools=self.visible)
        self.peer = peer
        self.owner = owner
        self.projection = {'tools': [], 'visibleTools': []}
        self.response = 0
        self.proposals = {}

    async def visible(self, ctx):
        return set(self.projection['visibleTools'])

    async def after_model_request(self, ctx, *, request_context, response):
        response = await super().after_model_request(ctx, request_context=request_context, response=response)
        # Inspect native typed output before the SDK's empty-output retry/error
        # branch. Reasoning is live-only, never a successful nocx answer. Preserve
        # provider failures such as length/content filtering as provider errors.
        if response.finish_reason in (None, 'stop') and all(
            isinstance(part, ThinkingPart) or (isinstance(part, TextPart) and not part.content.strip())
            for part in response.parts
        ):
            raise EmptyAnswer('Model returned no answer')
        self.response += 1
        batch = []
        for call in response.parts:
            if not isinstance(call, ToolCallPart):
                continue
            if isinstance(call.args, str):
                raw = call.args
            elif call.args is None:
                raw = ''
            else:
                # SDK Chat calls normally contain strings. A decoded object is
                # serialized exactly once, retaining Python's arbitrary-size ints.
                raw = json.dumps(call.args, ensure_ascii=False, separators=(',', ':'), allow_nan=False)
            if len(raw.encode('utf-8')) > ARGS_LIMIT:
                raise ProtocolError('Raw tool arguments exceed bound')
            batch.append({'callId': call.tool_call_id, 'name': call.tool_name, 'rawArgs': raw})
        for item in batch:
            self.proposals[item['callId']] = (self.response, item, batch)
        return response

    async def invoke(self, ctx, call):
        proposal = self.proposals.get(call.tool_call_id)
        if proposal is None:
            raise ProtocolError('Missing response call binding')
        ordinal, item, batch = proposal
        try:
            result = await self.peer.request('tool.invoke', 'tool.result', {
                'ownerRequestId': self.owner, 'response': ordinal, **item, 'batch': batch,
            })
        finally:
            # Retain the shared batch only while its sequential siblings are
            # outstanding. Compact call-ID fencing lives in HostCalls.
            self.proposals.pop(call.tool_call_id, None)
        # The Go host has already classified, framed, and screened these bytes.
        return result['text']


class HostToolset(AbstractToolset[DeepAgentDeps]):
    def __init__(self, calls):
        self.calls = calls
        self.validator = SchemaValidator(core_schema.any_schema())

    @property
    def id(self):
        return 'nocx-host'

    async def get_tools(self, ctx):
        projection = await self.calls.peer.request('catalog.request', 'catalog.result', {
            'ownerRequestId': self.calls.owner,
        })
        self.calls.projection = projection
        return {
            item['name']: ToolsetTool(
                toolset=self,
                tool_def=ToolDefinition(
                    name=item['name'], description=item['description'],
                    parameters_json_schema=item['parameters'], sequential=True,
                ),
                max_retries=0,
                args_validator=self.validator,
            )
            for item in projection['tools']
        }

    async def call_tool(self, name, tool_args, ctx, tool):
        call = self.calls.take(ctx, name)
        return await self.calls.invoke(ctx, call)


class Peer:
    def __init__(self, wire, manifest_digest):
        self.wire = wire
        self.manifest_digest = manifest_digest
        self.pending = {}
        self.streams = {}
        self.incoming_ids = set()
        self.tasks = {}
        self.generations = set()
        self.started = set()
        self.cancelled = set()
        self.terminal = set()
        self.start_id = None
        self.counter = 0
        self.closing = False
        self.failed = False
        self.reader_task = None
        self.handshaken = False

    def next_request_id(self):
        self.counter += 1
        number(self.counter, 1)
        return f'p:{self.counter}'

    async def notify(self, kind, payload):
        if self.closing:
            raise EOFError('Private peer closed')
        await self.wire.send(kind, None, payload)

    async def request(self, kind, expected, payload, *, request_id=None):
        if self.closing:
            raise EOFError('Private peer closed')
        identity = request_id or self.next_request_id()
        if identity in self.pending:
            raise ProtocolError('Repeated outgoing request')
        future = asyncio.get_running_loop().create_future()
        pending = Pending(expected, future)
        self.pending[identity] = pending

        def committed():
            pending.sent = True
            if kind == 'model.open':
                self.streams[identity].opened = True

        try:
            await self.wire.send(kind, identity, payload, on_commit=committed)
            return await future
        except BaseException:
            # Never retain an unsent request or close an unpublished model ID.
            # A committed cancelled request retains its one-reply registration.
            if not pending.sent:
                self.pending.pop(identity, None)
            future.cancel()
            raise

    def _register(self, identity):
        request_id(identity, 'g')
        if identity in self.incoming_ids:
            raise ProtocolError('Reused host request')
        self.incoming_ids.add(identity)

    def _reply(self, kind, identity, payload):
        request_id(identity, 'p')
        pending = self.pending.get(identity)
        if pending is None or kind not in (pending.expected, 'error'):
            raise ProtocolError('Unexpected reply')
        error = None
        if kind == 'error':
            fields(payload, {'code', 'status'})
            if payload['code'] not in ('protocol', 'provider', 'empty_answer', 'cancelled', 'host'):
                raise ProtocolError('Invalid error category')
            if payload['status'] is not None:
                number(payload['status'], 0, 599)
            error = RemoteError(payload['code'], payload['status'])
        elif kind == 'catalog.result':
            catalog(payload)
        elif kind == 'tool.result':
            fields(payload, {'text', 'kind'})
            text(payload['text'])
            if payload['kind'] not in ('tool_output', 'nocx_message'):
                raise ProtocolError('Invalid tool result classification')
        elif kind == 'model.headers':
            stream = self.streams.get(identity)
            if stream is None:
                raise ProtocolError('Unknown HTTP stream')
            stream.accept_headers(payload)
        del self.pending[identity]
        if not pending.future.done():
            if error is not None:
                pending.future.set_exception(error)
            else:
                pending.future.set_result(payload)

    async def _receive(self, kind, identity, payload):
        if kind in ('catalog.result', 'tool.result', 'model.headers', 'error'):
            self._reply(kind, identity, payload)
            return True
        if kind in ('model.chunk', 'model.end'):
            if identity is not None:
                raise ProtocolError('HTTP stream frame must be a notification')
            stream_id = request_id(payload.get('streamId'), 'p')
            stream = self.streams.get(stream_id)
            if stream is None:
                raise ProtocolError('Unknown HTTP stream')
            if kind == 'model.chunk':
                stream.chunk(payload)
            else:
                stream.end(payload)
            return True
        if kind == 'cancel':
            if identity is not None:
                raise ProtocolError('Cancel must be a notification')
            fields(payload, {'ownerRequestId'})
            owner = payload['ownerRequestId']
            if owner is None or owner == self.start_id:
                return False
            request_id(owner, 'g')
            if owner not in self.generations:
                raise ProtocolError('Unknown cancellation owner')
            self._cancel(owner)
            return True
        if kind not in ('start', 'generate'):
            raise ProtocolError('Unsupported incoming message')
        self._register(identity)
        expected = {'model', 'messages'} | ({'tools', 'visibleTools'} if kind == 'start' else set())
        fields(payload, expected)
        text(payload['model'], nonempty=True)
        messages(payload['messages'])
        if kind == 'start':
            if self.start_id is not None:
                raise ProtocolError('Repeated agent start')
            catalog({'tools': payload['tools'], 'visibleTools': payload['visibleTools']})
            self.start_id = identity
        else:
            self.generations.add(identity)
        self.tasks[identity] = asyncio.create_task(self._serve(kind, identity, payload))
        self.tasks[identity].add_done_callback(self._task_done)
        return True

    def _cancel(self, owner):
        if owner in self.terminal:
            return
        self.cancelled.add(owner)
        if owner in self.started and (task := self.tasks.get(owner)):
            task.cancel()

    def _task_done(self, task):
        # Retrieve every task exception so asyncio never prints raw SDK state.
        if not task.cancelled() and task.exception() is not None:
            self.failed = True
            if self.reader_task is not None:
                self.reader_task.cancel()

    async def _finish(self, kind, owner, payload):
        if owner in self.terminal:
            raise ProtocolError('Repeated operation completion')
        # Commit to one outcome before writing; cancellation may race drain().
        self.terminal.add(owner)
        await self.wire.send(kind, owner, payload)

    async def _events(self, owner, kind, value):
        # Bound action frames without splitting UTF-8 code points. Native part
        # identities/order are retained by the SDK; only approved deltas cross.
        data = value.encode('utf-8')
        offset = 0
        while offset < len(data):
            end = min(offset + CHUNK_LIMIT, len(data))
            while end < len(data) and data[end] & 0xC0 == 0x80:
                end -= 1
            chunk = data[offset:end].decode('utf-8')
            await self.notify('event', {'ownerRequestId': owner, 'kind': kind, 'text': chunk})
            offset = end

    async def _ask(self, owner, payload, model):
        calls = BridgeCalls(self, owner)
        calls.projection = {'tools': payload['tools'], 'visibleTools': payload['visibleTools']}
        agent = create_host_agent(model, instructions='', toolset=HostToolset(calls), calls=calls)
        with ToolManager.parallel_execution_mode('sequential'):
            async with agent.run_stream_events(
                None, message_history=history(payload['messages']), deps=DeepAgentDeps(),
                usage_limits=UsageLimits(request_limit=20),
            ) as events:
                async for event in events:
                    if isinstance(event, PartStartEvent):
                        if isinstance(event.part, TextPart):
                            await self._events(owner, 'answer', event.part.content)
                        elif isinstance(event.part, ThinkingPart):
                            await self._events(owner, 'reasoning', event.part.content)
                    elif isinstance(event, PartDeltaEvent):
                        if isinstance(event.delta, TextPartDelta):
                            await self._events(owner, 'answer', event.delta.content_delta)
                        elif isinstance(event.delta, ThinkingPartDelta) and event.delta.content_delta:
                            await self._events(owner, 'reasoning', event.delta.content_delta)
                    elif isinstance(event, AgentRunResultEvent):
                        return event.result.output
        raise RuntimeError('SDK run returned no result')

    async def _generate(self, payload, model):
        async with model.request_stream(history(payload['messages']), None, ModelRequestParameters()) as stream:
            async for _event in stream:
                pass
            result = stream.get()
        # Generation must never dispatch a model-authored tool or reveal its raw
        # request as prose. The native adapter still owns all parsing.
        if any(isinstance(part, ToolCallPart) for part in result.parts):
            raise RuntimeError('Tool call in tool-free generation')
        return ''.join(part.content for part in result.parts if isinstance(part, TextPart))

    async def _serve(self, kind, owner, payload):
        try:
            self.started.add(owner)
            if owner in self.cancelled:
                raise asyncio.CancelledError
            async with native_model(self, owner, payload['model']) as model:
                output = await (self._ask(owner, payload, model) if kind == 'start'
                                else self._generate(payload, model))
            if not isinstance(output, str) or not output.strip():
                await self._finish('error', owner, {'code': 'empty_answer', 'status': None})
            else:
                await self._finish('complete' if kind == 'start' else 'generated', owner, {'text': output})
        except asyncio.CancelledError:
            if not self.closing and owner not in self.terminal:
                await self._finish('error', owner, {'code': 'cancelled', 'status': None})
        except Exception as error:
            if owner in self.terminal:
                raise
            safe = safe_failure(error)
            if not self.closing:
                await self._finish('error', owner, safe)
            if safe['code'] == 'protocol':
                self.failed = True
                if self.reader_task is not None:
                    self.reader_task.cancel()
        finally:
            self.tasks.pop(owner, None)

    async def run(self):
        self.reader_task = asyncio.current_task()
        try:
            kind, identity, payload = await self.wire.receive()
            if kind != 'hello':
                raise ProtocolError('Handshake required')
            self._register(identity)
            fields(payload, {'manifestDigest', 'bridgeVersion'})
            if (payload['manifestDigest'] != self.manifest_digest
                    or type(payload['bridgeVersion']) is not int or payload['bridgeVersion'] != VERSION):
                raise ProtocolError('Wrong runtime handshake')
            await self.wire.send('hello', identity, payload)
            self.handshaken = True
            while await self._receive(*(await self.wire.receive())):
                pass
        except EOFError:
            self.closing = True
        except asyncio.CancelledError:
            if not self.failed:
                raise
        except Exception:
            self.failed = True
            self.closing = True
        finally:
            tasks = list(self.tasks.values())
            for owner in list(self.tasks):
                self._cancel(owner)
            await asyncio.gather(*tasks, return_exceptions=True)
            self.closing = True
            for pending in self.pending.values():
                pending.future.cancel()
            for stream in self.streams.values():
                stream.abort()
            self.wire.close()
        return 2 if self.failed else 0
