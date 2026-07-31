import 'package:flutter_test/flutter_test.dart';
import 'package:opendray/features/agent_tasks/data/agent_tasks_stream.dart';
import 'package:opendray/features/agent_tasks/domain/agent_task_models.dart';

void main() {
  test('WebSocket URI preserves gateway prefix and opaque cursor', () {
    final client = AgentTaskStreamClient(
      serverUrl: 'https://gateway.example/prefix/',
      token: 'token',
    );
    final uri = client.buildUri('/api/v1/oneshot/runs/orn_1/stream', {
      'project_id': '/repo',
      'cursor': 'opaque-cursor',
    });

    expect(uri.scheme, 'wss');
    expect(uri.path, '/prefix/api/v1/oneshot/runs/orn_1/stream');
    expect(uri.queryParameters['project_id'], '/repo');
    expect(uri.queryParameters['cursor'], 'opaque-cursor');
  });

  test('Cursor tracker advances before suppressing a replay duplicate', () {
    final tracker = AgentStreamCursorTracker(initialCursor: 'cursor-0');
    final first = AgentStreamFrame(
      topic: 'oneshot.run.output',
      occurredAt: DateTime.utc(2026, 7, 28),
      cursor: 'cursor-1',
      data: const {'event_id': 'ose_1'},
    );
    final duplicate = AgentStreamFrame(
      topic: 'oneshot.run.output',
      occurredAt: DateTime.utc(2026, 7, 28),
      cursor: 'cursor-1',
      data: const {'event_id': 'ose_1'},
    );

    expect(tracker.accept(first), isTrue);
    expect(tracker.lastCursor, 'cursor-1');
    expect(tracker.accept(duplicate), isFalse);
    expect(tracker.lastCursor, 'cursor-1');
  });

  test('Different cursor identities are accepted in order', () {
    final tracker = AgentStreamCursorTracker();
    for (var i = 1; i <= 3; i++) {
      expect(
        tracker.accept(
          AgentStreamFrame(
            topic: 'oneshot.run.output',
            occurredAt: DateTime.utc(2026, 7, 28, 0, 0, i),
            cursor: 'cursor-$i',
            data: {'event_id': 'ose_$i'},
          ),
        ),
        isTrue,
      );
    }
    expect(tracker.lastCursor, 'cursor-3');
  });

  test('Stream error mapper preserves forbidden and retry metadata', () {
    final error = AgentStreamErrorMapper.fromData({
      'error': {
        'code': 'oneshot.forbidden',
        'message': 'scope missing',
        'request_id': 'req-stream-1',
        'retryable': false,
        'details': {'scope': 'event:subscribe:oneshot.run.*'},
      },
    });

    expect(error.isForbidden, isTrue);
    expect(error.retryable, isFalse);
    expect(error.requestId, 'req-stream-1');
  });

  test('Malformed WebSocket frames are rejected locally', () {
    expect(
      () => AgentTaskStreamClient.decodeFrame('[]'),
      throwsA(isA<FormatException>()),
    );
    expect(
      () => AgentTaskStreamClient.decodeFrame('{not-json'),
      throwsA(isA<FormatException>()),
    );
  });

  test('Valid WebSocket frames decode without touching reconnect state', () {
    final frame = AgentTaskStreamClient.decodeFrame(
      '{"topic":"oneshot.run.output","ts":"2026-07-28T00:00:00Z","cursor":"cursor-1","data":{"event_id":"ose_1"}}',
    );
    expect(frame.topic, 'oneshot.run.output');
    expect(frame.cursor, 'cursor-1');
  });
}
