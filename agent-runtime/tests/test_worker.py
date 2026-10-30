"""Application-specific interpretation of native SDK response parts."""
import unittest

from pydantic_ai.messages import ModelResponse, TextPart, ThinkingPart, ToolCallPart

from nocx_agent_runtime.worker import BridgeCalls, EmptyAnswer, safe_failure


class ResponseTests(unittest.IsolatedAsyncioTestCase):
    async def test_normal_stop_without_answer_is_empty_not_provider_failure(self):
        for parts in ([], [ThinkingPart('reasoning')], [TextPart('  ')],
                      [ThinkingPart('reasoning'), TextPart('')]):
            with self.subTest(parts=parts):
                calls = BridgeCalls(None, 'g:2')
                with self.assertRaises(EmptyAnswer) as error:
                    await calls.after_model_request(
                        None, request_context=None, response=ModelResponse(parts=parts, finish_reason='stop'),
                    )
                self.assertEqual(safe_failure(error.exception), {'code': 'empty_answer', 'status': None})

    async def test_provider_failure_and_actionable_output_keep_native_path(self):
        for finish, parts in (
            ('length', [ThinkingPart('reasoning')]),
            ('content_filter', [ThinkingPart('reasoning')]),
            ('stop', [TextPart('answer')]),
            ('tool_call', [ToolCallPart(tool_name='unknown', tool_call_id='original', args='{bad')]),
        ):
            with self.subTest(finish=finish, parts=parts):
                calls = BridgeCalls(None, 'g:2')
                response = ModelResponse(parts=parts, finish_reason=finish)
                self.assertIs(await calls.after_model_request(None, request_context=None, response=response), response)


if __name__ == '__main__':
    unittest.main()
