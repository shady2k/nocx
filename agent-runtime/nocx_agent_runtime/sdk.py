"""Nocx-only SDK wiring; host callbacks remain the sole effect authority.

Create one HostCalls per live run. Run the returned agent task inside
ToolManager.parallel_execution_mode('sequential'), with deps=DeepAgentDeps().
A pending callback is the approval barrier, not an SDK deferred-tool result.
"""
from collections.abc import Awaitable, Callable
from typing import Any
from dataclasses import replace

from pydantic_ai import Agent, RunContext
from pydantic_ai.capabilities import AbstractCapability
from pydantic_ai.exceptions import SkipToolValidation
from pydantic_ai.messages import ModelResponse, ToolCallPart
from pydantic_ai.models import Model
from pydantic_ai.toolsets import AbstractToolset
from pydantic_deep import DeepAgentDeps, create_deep_agent


class HostCalls(AbstractCapability):
    """Capture raw calls without parsing and reject identity reuse before effects."""

    def __init__(
        self,
        unknown: Callable[[RunContext[Any], ToolCallPart], Awaitable[Any]],
        *,
        visible_tools: Callable[[RunContext[Any]], Awaitable[set[str]]] | None = None,
    ):
        self._unknown = unknown
        self._visible_tools = visible_tools
        self._calls: dict[str, ToolCallPart] = {}
        self._consumed: set[str] = set()

    async def before_model_request(self, ctx, request_context):
        if self._visible_tools is None:
            return request_context
        visible = await self._visible_tools(ctx)
        parameters = request_context.model_request_parameters
        # Filter only the provider's catalog. Prepared ToolManager definitions
        # retain all host-admitted tools, including known lazy/hidden names.
        return replace(request_context, model_request_parameters=replace(
            parameters, function_tools=[tool for tool in parameters.function_tools if tool.name in visible],
        ))

    async def after_model_request(self, ctx, *, request_context, response: ModelResponse):
        calls = [part for part in response.parts if isinstance(part, ToolCallPart)]
        ids = [call.tool_call_id for call in calls]
        if any(not call_id for call_id in ids) or len(set(ids)) != len(ids):
            raise RuntimeError('Missing or duplicate tool call identity')
        if any(call_id in self._calls or call_id in self._consumed for call_id in ids):
            raise RuntimeError('Reused tool call identity')
        self._calls.update((call.tool_call_id, call) for call in calls)
        return response

    async def before_tool_validate(self, ctx, *, call, tool_def, args):
        captured = self._calls.get(call.tool_call_id)
        expected_args = call.args if call.args is not None else {}
        if captured is not call or call.tool_name != tool_def.name or args != expected_args:
            raise RuntimeError('Tool call identity or raw arguments changed')
        raise SkipToolValidation(validated_args={})

    def take(self, ctx: RunContext[Any], name: str) -> ToolCallPart:
        """Bind a single SDK invocation to the original response call.

        A host resume resolves the same pending callback; it must not call take again.
        """
        call_id = ctx.tool_call_id
        call = self._calls.get(call_id)
        if call is None or call.tool_name != name or ctx.tool_name != name or call_id in self._consumed:
            raise RuntimeError('Unbound or repeated tool invocation')
        self._consumed.add(call_id)
        return self._calls.pop(call_id)

    async def unknown(self, ctx: RunContext[Any], call: ToolCallPart) -> Any:
        if self.take(ctx, call.tool_name) is not call:
            raise RuntimeError('Unknown call identity changed')
        return await self._unknown(ctx, call)


def create_host_agent(
    model: Model, *, instructions: str, toolset: AbstractToolset[DeepAgentDeps], calls: HostCalls
) -> Agent[DeepAgentDeps, str]:
    """Construct the real factory with every competing capability disabled."""
    agent = create_deep_agent(
        model,
        instructions=instructions,
        toolsets=[toolset],
        capabilities=[calls],
        unknown_tool_handler=calls.unknown,
        workspace=False,
        output_type=str,
        end_strategy='graceful',
        retries=0,
        context_manager=False,
        eviction_token_limit=None,
        patch_tool_calls=False,
        stuck_loop_detection=False,
        cost_tracking=False,
        thinking=False,
        forking=False,
        tool_search=False,
        web_search=False,
        web_fetch=False,
        include_filesystem=False,
        include_execute=False,
        include_todo=False,
        include_current_todos=False,
        include_subagents=False,
        include_builtin_subagents=False,
        include_plan=False,
        include_skills=False,
        include_memory=False,
        include_teams=False,
        include_monitoring=False,
        include_checkpoints=False,
        include_improve=False,
        include_liteparse=False,
        include_history_archive=False,
    )
    # This pinned factory forwards unknown kwargs to Agent, whose constructor no
    # longer accepts instrument. The supported property is set before any run.
    agent.instrument = False
    return agent
