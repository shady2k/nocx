"""Native SDK model configuration for nocx's generic Chat-compatible endpoint."""
from contextlib import asynccontextmanager

import httpx2
from openai import AsyncOpenAI
from pydantic_ai.exceptions import UnexpectedModelBehavior
from pydantic_ai.messages import ModelRequest, ModelResponse, SystemPromptPart, TextPart, UserPromptPart
from pydantic_ai.models.openai import OpenAIChatModel, OpenAIStreamedResponse
from pydantic_ai.profiles import DEFAULT_PROFILE
from pydantic_ai.profiles.openai import OpenAIModelProfile
from pydantic_ai.providers.openai import OpenAIProvider

from .transport import GoIPCTransport

# No provider/model-name capability guesses. The provider exposes the same
# profile so base-class resolution cannot infer a context window from prices.
GENERIC_PROFILE = OpenAIModelProfile(
    **{**DEFAULT_PROFILE,
       'supported_native_tools': frozenset(),
       'json_schema_transformer': None,
       'context_window': None,
       'supports_inline_system_prompts': True,
       'supports_thinking': False,
       'thinking_always_enabled': False,
       'thinking_enabled_by_default': False},
    openai_system_prompt_role='system',
    openai_supports_strict_tool_definition=False,
    openai_supports_reasoning=False,
    openai_chat_send_back_thinking_parts=False,
)


class GenericProvider(OpenAIProvider):
    def model_profile(self, model_name):
        return GENERIC_PROFILE


class LiteralTextStream(OpenAIStreamedResponse):
    async def _validate_response(self):
        # Pinned SDK's documented chunk-validation hook (openai.py:4130-4137),
        # selected via its documented _streamed_response_cls construction seam.
        # Inspect already-parsed native chunks, never parse or rewrite SSE. The
        # SDK may synthesize an interim ToolCallPart ID before a later delta
        # supplies the provider ID; only completed-stream absence is invalid.
        original_ids = {}
        async for chunk in super()._validate_response():
            if chunk.choices and chunk.choices[0].delta is not None:
                for call in chunk.choices[0].delta.tool_calls or ():
                    if type(call.index) is not int or call.index < 0:
                        raise UnexpectedModelBehavior('Provider returned an invalid tool call index')
                    index = call.index
                    previous = original_ids.setdefault(index, None)
                    if call.id:
                        if type(call.id) is not str:
                            raise UnexpectedModelBehavior('Provider returned an invalid tool call identity')
                        identity = call.id
                        if previous is not None and previous != identity:
                            raise UnexpectedModelBehavior('Provider changed a tool call identity')
                        original_ids[index] = identity
            yield chunk
        if any(identity is None for identity in original_ids.values()):
            raise UnexpectedModelBehavior('Provider omitted a final tool call identity')

    def _map_text_delta(self, choice):
        # Documented native stream mapping extension: preserve literal text tags.
        # Reasoning fields and tool calls still use the untouched SDK mappings,
        # and the native OpenAI SDK still parses every SSE byte.
        if choice.delta.content:
            yield from self._parts_manager.handle_text_delta(
                vendor_part_id=self._vendor_part_id,
                content=choice.delta.content,
            )


class ChatModel(OpenAIChatModel):
    @property
    def _streamed_response_cls(self):
        return LiteralTextStream


@asynccontextmanager
async def native_model(peer, owner, model_name):
    async with httpx2.AsyncClient(
        transport=GoIPCTransport(peer, owner), trust_env=False, follow_redirects=False,
    ) as http:
        async with AsyncOpenAI(
            api_key='nocx-placeholder', base_url='https://nocx.invalid/v1/',
            http_client=http, max_retries=0,
        ) as sdk:
            yield ChatModel(model_name, provider=GenericProvider(openai_client=sdk), profile=GENERIC_PROFILE)


def history(messages):
    """Map host-assembled text history once; never add a duplicate run prompt."""
    result = []
    for message in messages:
        role, content = message['role'], message['content']
        if role == 'assistant':
            result.append(ModelResponse(parts=[TextPart(content)]))
        elif role == 'system':
            result.append(ModelRequest(parts=[SystemPromptPart(content)]))
        else:
            result.append(ModelRequest(parts=[UserPromptPart(content)]))
    return result
