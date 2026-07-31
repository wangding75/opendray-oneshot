import 'package:flutter_test/flutter_test.dart';
import 'package:opendray/features/agent_tasks/domain/agent_task_models.dart';

void main() {
  test('Task and Run status parsers cover the frozen lifecycle', () {
    const taskStatuses = {
      'pending': AgentTaskStatus.pending,
      'queued': AgentTaskStatus.queued,
      'running': AgentTaskStatus.running,
      'waiting_input': AgentTaskStatus.waitingInput,
      'completed': AgentTaskStatus.completed,
      'failed': AgentTaskStatus.failed,
      'cancelled': AgentTaskStatus.cancelled,
      'timed_out': AgentTaskStatus.timedOut,
    };
    for (final entry in taskStatuses.entries) {
      expect(AgentTaskStatus.parse(entry.key), entry.value);
      expect(entry.value.wire, entry.key);
    }
    expect(AgentTaskStatus.completed.canContinue, isTrue);
    expect(AgentTaskStatus.completed.canRetry, isFalse);
    expect(AgentTaskStatus.failed.canRetry, isTrue);

    const runStatuses = {
      'created': AgentRunStatus.created,
      'starting': AgentRunStatus.starting,
      'running': AgentRunStatus.running,
      'collecting_output': AgentRunStatus.collectingOutput,
      'waiting_input': AgentRunStatus.waitingInput,
      'completed': AgentRunStatus.completed,
      'failed': AgentRunStatus.failed,
      'cancelled': AgentRunStatus.cancelled,
      'timed_out': AgentRunStatus.timedOut,
    };
    for (final entry in runStatuses.entries) {
      expect(AgentRunStatus.parse(entry.key), entry.value);
      expect(entry.value.wire, entry.key);
    }
  });

  test('Telegram source parses immutable reply routing fields', () {
    final source = AgentTaskSource.fromJson({
      'kind': 'telegram',
      'channel_id': 'telegram',
      'source_message_id': '101',
      'client_request_id': 'update-7',
      'reply_address': {
        'channel_id': 'telegram',
        'conversation_id': '-100123',
        'thread_id': '44',
        'message_id': '102',
      },
    });

    expect(source.kind, 'telegram');
    expect(source.channelId, 'telegram');
    expect(source.conversationId, '-100123');
    expect(source.threadId, '44');
    expect(source.messageId, '101');
    expect(source.clientRequestId, 'update-7');
  });

  test('Provider manifest maps only supported One-shot capabilities', () {
    final capability = AgentProviderCapability.fromProviderGatewayJson({
      'enabled': true,
      'manifest': {
        'id': 'claude',
        'displayName': 'Claude Code',
        'capabilities': {
          'supportsResume': true,
          'supportsStream': true,
          'supportsImages': true,
        },
      },
      'oneshot': {
        'provider_id': 'claude-code',
        'display_name': 'Claude Code',
        'enabled': true,
        'capabilities': {
          'supports_non_interactive': true,
          'supports_resume': true,
          'structured_output': true,
          'attachments': false,
          'cancellation': true,
        },
      },
    });

    expect(capability.providerId, 'claude-code');
    expect(capability.enabled, isTrue);
    expect(capability.supportsNonInteractive, isTrue);
    expect(capability.supportsResume, isTrue);
    expect(capability.structuredOutput, isTrue);
    expect(capability.attachments, isFalse);
    expect(capability.cancellation, isTrue);
  });

  test('PTY image capability never enables One-shot attachments by fallback', () {
    final capability = AgentProviderCapability.fromProviderGatewayJson({
      'enabled': true,
      'manifest': {
        'id': 'claude',
        'displayName': 'Claude Code',
        'capabilities': {'supportsImages': true},
      },
    });

    expect(capability.supportsNonInteractive, isTrue);
    expect(capability.attachments, isFalse);
  });

  test('Event timeline orders, deduplicates, and keeps the newest bound', () {
    final base = DateTime.utc(2026, 7, 28, 12);
    AgentEvent event(String id, int sequence, int second) => AgentEvent(
          id: id,
          runId: 'orn_1',
          sequence: sequence,
          type: 'assistant.delta',
          content: {'stream': 'stdout', 'text': id},
          occurredAt: base.add(Duration(seconds: second)),
        );

    final merged = AgentEventTimeline.merge(
      [event('e2', 2, 2), event('e1', 1, 1), event('e2', 2, 3)],
      maxEvents: 2,
    );

    expect(merged.map((item) => item.id), ['e1', 'e2']);
    expect(merged.last.occurredAt, base.add(const Duration(seconds: 3)));
  });

  test('Create and Continue payloads remain separate execution operations', () {
    const create = CreateAgentTaskInput(
      projectId: '/repo',
      providerId: 'codex',
      prompt: 'Implement feature',
      workspacePath: '/repo',
      attachmentRefs: ['oar_1'],
      timeoutSeconds: 120,
      telegramNotify: true,
    );
    final createJson = create.toJson('mobile-key');
    expect(createJson['source'], {
      'kind': 'mobile',
      'client_request_id': 'mobile-key',
    });
    expect(createJson['timeout_seconds'], 120);
    expect((createJson['options'] as Map)['context_mode'], 'new');

    const continuation = ContinueAgentTaskInput(
      projectId: '/repo',
      providerId: 'claude-code',
      workspacePath: '/repo',
      promptDelta: 'Continue from the current context',
      attachmentRefs: ['oar_2'],
    );
    final continueJson = continuation.toJson();
    expect(continueJson['prompt_delta'], isNotEmpty);
    expect(continueJson.containsKey('timeout_seconds'), isFalse);
    expect(continueJson.containsKey('source'), isFalse);
  });
}
