import 'dart:async';
import 'dart:convert';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:opendray/core/auth/auth_state.dart';
import 'package:opendray/features/agent_tasks/data/agent_tasks_api.dart';
import 'package:opendray/features/agent_tasks/domain/agent_task_models.dart';
import 'package:web_socket_channel/io.dart';
import 'package:web_socket_channel/web_socket_channel.dart';

typedef AgentSocketConnector =
    WebSocketChannel Function(Uri uri, Map<String, dynamic> headers);

class AgentStreamCursorTracker {
  AgentStreamCursorTracker({String? initialCursor})
    : lastCursor = initialCursor ?? '';

  String lastCursor;
  final Set<String> _seen = <String>{};

  bool accept(AgentStreamFrame frame) {
    if (frame.cursor.isNotEmpty) lastCursor = frame.cursor;
    final accepted = _seen.add(frame.identity);
    if (_seen.length > 4096) _seen.remove(_seen.first);
    return accepted;
  }
}

class AgentTaskStreamClient {
  AgentTaskStreamClient({
    required this.serverUrl,
    required this.token,
    this.initialBackoff = const Duration(milliseconds: 250),
    this.maxBackoff = const Duration(seconds: 8),
    AgentSocketConnector? connect,
  }) : _connect =
           connect ??
           ((uri, headers) => IOWebSocketChannel.connect(
             uri,
             headers: headers,
             pingInterval: const Duration(seconds: 20),
           ));

  final String serverUrl;
  final String token;
  final Duration initialBackoff;
  final Duration maxBackoff;
  final AgentSocketConnector _connect;

  Stream<AgentStreamFrame> taskStream({String? projectId, String? cursor}) =>
      _reconnectingStream(
        path: '/api/v1/oneshot/tasks/stream',
        query: {
          if (projectId != null && projectId.isNotEmpty)
            'project_id': projectId,
        },
        initialCursor: cursor,
      );

  Stream<AgentStreamFrame> runStream(
    String runId, {
    String? projectId,
    String? cursor,
  }) => _reconnectingStream(
    path: '/api/v1/oneshot/runs/$runId/stream',
    query: {
      if (projectId != null && projectId.isNotEmpty) 'project_id': projectId,
    },
    initialCursor: cursor,
  );

  Stream<AgentStreamFrame> _reconnectingStream({
    required String path,
    required Map<String, String> query,
    String? initialCursor,
  }) {
    late StreamController<AgentStreamFrame> controller;
    WebSocketChannel? channel;
    Timer? reconnectTimer;
    var stopped = false;
    final tracker = AgentStreamCursorTracker(initialCursor: initialCursor);
    var backoff = initialBackoff;
    late void Function() scheduleReconnect;

    Future<void> connect() async {
      if (stopped) return;
      final uri = buildUri(path, {
        ...query,
        if (tracker.lastCursor.isNotEmpty) 'cursor': tracker.lastCursor,
      });
      try {
        channel = _connect(uri, {'Authorization': 'Bearer $token'});
        await channel!.ready;
        backoff = initialBackoff;
        channel!.stream.listen(
          (raw) {
            if (raw is! String) return;
            AgentStreamFrame frame;
            try {
              frame = decodeFrame(raw);
            } on Object catch (error, stack) {
              if (!stopped && !controller.isClosed) {
                controller.addError(error, stack);
              }
              return;
            }
            if (frame.topic == 'oneshot.stream.error') {
              final error = AgentStreamErrorMapper.fromData(frame.data);
              if (!controller.isClosed) controller.addError(error);
              unawaited(channel?.sink.close());
              if (error.isUnauthorized ||
                  error.isForbidden ||
                  !error.retryable) {
                stopped = true;
                reconnectTimer?.cancel();
                if (!controller.isClosed) unawaited(controller.close());
              } else {
                scheduleReconnect();
              }
              return;
            }
            if (tracker.accept(frame) && !controller.isClosed) {
              controller.add(frame);
            }
          },
          onError: (Object error, StackTrace stack) {
            if (stopped || controller.isClosed) return;
            controller.addError(error, stack);
            unawaited(channel?.sink.close());
            scheduleReconnect();
          },
          onDone: scheduleReconnect,
          cancelOnError: false,
        );
      } on Object catch (error, stack) {
        if (!stopped && !controller.isClosed) {
          controller.addError(error, stack);
          scheduleReconnect();
        }
      }
    }

    scheduleReconnect = () {
      if (stopped || (reconnectTimer?.isActive ?? false)) return;
      reconnectTimer = Timer(backoff, connect);
      final nextMillis = (backoff.inMilliseconds * 2).clamp(
        initialBackoff.inMilliseconds,
        maxBackoff.inMilliseconds,
      );
      backoff = Duration(milliseconds: nextMillis);
    };

    controller = StreamController<AgentStreamFrame>(
      onListen: () => unawaited(connect()),
      onCancel: () async {
        stopped = true;
        reconnectTimer?.cancel();
        await channel?.sink.close();
      },
    );
    return controller.stream;
  }

  static AgentStreamFrame decodeFrame(String raw) {
    final decoded = jsonDecode(raw);
    if (decoded is! Map) {
      throw const FormatException('One-shot stream frame is not an object');
    }
    return AgentStreamFrame.fromJson(Map<String, dynamic>.from(decoded));
  }

  Uri buildUri(String path, Map<String, String> query) {
    final base = Uri.parse(serverUrl);
    final scheme = base.scheme == 'https' ? 'wss' : 'ws';
    final basePath = base.path.endsWith('/')
        ? base.path.substring(0, base.path.length - 1)
        : base.path;
    return base.replace(
      scheme: scheme,
      path: '$basePath$path',
      queryParameters: query.isEmpty ? null : query,
    );
  }
}

class AgentStreamErrorMapper {
  const AgentStreamErrorMapper._();

  static AgentTasksApiException fromData(Map<String, dynamic> data) {
    final envelope = data['error'];
    final error = envelope is Map
        ? Map<String, dynamic>.from(envelope)
        : const <String, dynamic>{};
    final code = error['code']?.toString() ?? 'oneshot.stream_error';
    final statusCode = code.contains('unauthorized')
        ? 401
        : code.contains('forbidden')
        ? 403
        : 500;
    return AgentTasksApiException(
      statusCode: statusCode,
      code: code,
      message: error['message']?.toString() ?? 'One-shot stream failed',
      retryable: error['retryable'] == true,
      requestId: error['request_id']?.toString(),
      details: error['details'] is Map
          ? Map<String, dynamic>.from(error['details'] as Map)
          : const {},
    );
  }
}

final agentTaskStreamClientProvider = Provider<AgentTaskStreamClient?>((ref) {
  final auth = ref.watch(authControllerProvider);
  return switch (auth) {
    AuthLoggedIn(serverUrl: final serverUrl, token: final token) =>
      AgentTaskStreamClient(serverUrl: serverUrl, token: token),
    _ => null,
  };
});
