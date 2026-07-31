import 'dart:convert';
import 'dart:typed_data';

import 'package:crypto/crypto.dart';
import 'package:dio/dio.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:opendray/features/agent_tasks/data/agent_tasks_api.dart';
import 'package:opendray/features/agent_tasks/domain/agent_task_models.dart';

class _ResponseSpec {
  const _ResponseSpec(this.status, this.body, {this.headers = const {}});

  final int status;
  final Object body;
  final Map<String, List<String>> headers;
}

class _RecordingAdapter implements HttpClientAdapter {
  _RecordingAdapter(this.responses);

  final List<_ResponseSpec> responses;
  final List<RequestOptions> requests = [];

  @override
  Future<ResponseBody> fetch(
    RequestOptions options,
    Stream<Uint8List>? requestStream,
    Future<void>? cancelFuture,
  ) async {
    requests.add(options);
    final spec = responses.removeAt(0);
    final headers = <String, List<String>>{
      Headers.contentTypeHeader: ['application/json'],
      ...spec.headers,
    };
    if (spec.body is List<int>) {
      return ResponseBody.fromBytes(
        Uint8List.fromList(spec.body as List<int>),
        spec.status,
        headers: headers,
      );
    }
    return ResponseBody.fromString(
      jsonEncode(spec.body),
      spec.status,
      headers: headers,
    );
  }

  @override
  void close({bool force = false}) {}
}

Map<String, dynamic> _taskJson({String id = 'otk_1'}) => {
      'id': id,
      'project_id': '/repo',
      'provider_id': 'codex',
      'source': {'kind': 'mobile', 'client_request_id': 'mobile-1'},
      'prompt': 'Implement feature',
      'status': 'queued',
      'version': 2,
      'created_at': '2026-07-28T00:00:00Z',
      'updated_at': '2026-07-28T00:00:01Z',
    };

void main() {
  late Dio dio;

  AgentTasksApi apiFor(List<_ResponseSpec> responses) {
    dio = Dio(BaseOptions(baseUrl: 'https://gateway.example'));
    dio.httpClientAdapter = _RecordingAdapter(responses);
    return AgentTasksApi(dio);
  }

  test('Task list sends filters and parses frozen lower-case page fields', () async {
    final api = apiFor([
      _ResponseSpec(200, {
        'items': [_taskJson()],
        'next_cursor': 'opaque-next',
      }),
    ]);
    final page = await api.listTasks(
      projectId: '/repo',
      status: AgentTaskStatus.queued,
    );
    final request = (dio.httpClientAdapter as _RecordingAdapter).requests.single;

    expect(request.path, '/api/v1/oneshot/tasks');
    expect(request.queryParameters['project_id'], '/repo');
    expect(request.queryParameters['status'], 'queued');
    expect(page.items.single.id, 'otk_1');
    expect(page.nextCursor, 'opaque-next');
  });

  test('Task list tolerates the pre-fix Page field casing during rollout', () async {
    final api = apiFor([
      _ResponseSpec(200, {
        'Items': [_taskJson(id: 'otk_legacy')],
        'NextCursor': 'legacy-cursor',
      }),
    ]);
    final page = await api.listTasks();
    expect(page.items.single.id, 'otk_legacy');
    expect(page.nextCursor, 'legacy-cursor');
  });

  test('Create uses one Idempotency-Key in header and mobile source', () async {
    final api = apiFor([
      _ResponseSpec(202, {'task': _taskJson()}),
    ]);
    await api.createTask(
      const CreateAgentTaskInput(
        projectId: '/repo',
        providerId: 'codex',
        prompt: 'Implement feature',
        workspacePath: '/repo',
      ),
      idempotencyKey: 'mobile-idempotency-1',
    );
    final request = (dio.httpClientAdapter as _RecordingAdapter).requests.single;
    final body = request.data as Map<String, dynamic>;

    expect(request.method, 'POST');
    expect(request.path, '/api/v1/oneshot/tasks');
    expect(request.headers['Idempotency-Key'], 'mobile-idempotency-1');
    expect((body['source'] as Map)['client_request_id'], 'mobile-idempotency-1');
  });

  test('Continue and retry use distinct endpoints and payload semantics', () async {
    final api = apiFor([
      _ResponseSpec(202, {'task': _taskJson()}),
      _ResponseSpec(202, {'task': _taskJson()}),
    ]);
    await api.continueTask(
      'otk_1',
      const ContinueAgentTaskInput(
        projectId: '/repo',
        providerId: 'codex',
        promptDelta: 'Follow up',
      ),
      idempotencyKey: 'continue-1',
    );
    await api.retryTask(
      'otk_1',
      projectId: '/repo',
      idempotencyKey: 'retry-1',
      promptDelta: 'Try again',
    );
    final requests = (dio.httpClientAdapter as _RecordingAdapter).requests;

    expect(requests[0].path, '/api/v1/oneshot/tasks/otk_1/continue');
    expect((requests[0].data as Map)['prompt_delta'], 'Follow up');
    expect(requests[1].path, '/api/v1/oneshot/tasks/otk_1/retry');
    expect((requests[1].data as Map)['prompt_delta'], 'Try again');
  });

  test('Nested error envelope preserves permission and retry metadata', () async {
    final api = apiFor([
      const _ResponseSpec(403, {
        'error': {
          'code': 'oneshot.forbidden',
          'message': 'required scope is missing',
          'request_id': 'req-1',
          'retryable': false,
          'details': {'scope': 'oneshot:task:read'},
        },
      }),
    ]);

    await expectLater(
      api.getTask('otk_1'),
      throwsA(
        isA<AgentTasksApiException>()
            .having((error) => error.isForbidden, 'isForbidden', isTrue)
            .having((error) => error.requestId, 'requestId', 'req-1')
            .having((error) => error.retryable, 'retryable', isFalse),
      ),
    );
  });

  test('Artifact download verifies metadata, Digest, ETag, and length', () async {
    final bytes = utf8.encode('artifact-content');
    final digest = sha256.convert(bytes);
    final api = apiFor([
      _ResponseSpec(
        200,
        bytes,
        headers: {
          Headers.contentTypeHeader: ['text/plain'],
          Headers.contentLengthHeader: ['${bytes.length}'],
          'digest': ['sha-256=${base64Encode(digest.bytes)}'],
          'etag': ['"$digest"'],
        },
      ),
    ]);
    final download = await api.downloadArtifact(
      AgentArtifact(
        id: 'oar_1',
        taskId: 'otk_1',
        runId: 'orn_1',
        kind: 'result',
        name: 'result.txt',
        contentType: 'text/plain',
        sizeBytes: bytes.length,
        sha256: digest.toString(),
        createdAt: DateTime.utc(2026, 7, 28),
      ),
    );

    expect(download.integrityVerified, isTrue);
    expect(download.sha256, digest.toString());
    expect(download.bytes, bytes);
  });

  test('Artifact metadata size mismatch is rejected before save/open', () async {
    final bytes = utf8.encode('artifact-content');
    final digest = sha256.convert(bytes);
    final api = apiFor([
      _ResponseSpec(200, bytes),
    ]);
    final artifact = AgentArtifact(
      id: 'oar_size',
      taskId: 'otk_1',
      kind: 'result',
      name: 'result.txt',
      contentType: 'text/plain',
      sizeBytes: bytes.length + 1,
      sha256: digest.toString(),
      createdAt: DateTime.utc(2026, 7, 28),
    );

    await expectLater(
      api.downloadArtifact(artifact),
      throwsA(
        isA<AgentTasksApiException>().having(
          (error) => error.code,
          'code',
          'oneshot.artifact_integrity_failed',
        ),
      ),
    );
  });

  test('Artifact mismatch is rejected before save/open', () async {
    final bytes = utf8.encode('tampered');
    final api = apiFor([
      _ResponseSpec(200, bytes),
    ]);
    final artifact = AgentArtifact(
      id: 'oar_1',
      taskId: 'otk_1',
      kind: 'result',
      name: 'result.txt',
      contentType: 'text/plain',
      sizeBytes: bytes.length,
      sha256: List.filled(64, '0').join(),
      createdAt: DateTime.utc(2026, 7, 28),
    );

    await expectLater(
      api.downloadArtifact(artifact),
      throwsA(
        isA<AgentTasksApiException>().having(
          (error) => error.code,
          'code',
          'oneshot.artifact_integrity_failed',
        ),
      ),
    );
  });

  test('Attachment staging uses multipart owner-scoped endpoint', () async {
    final api = apiFor([
      const _ResponseSpec(201, {
        'attachment': {
          'id': 'oat_mobile1',
          'project_id': '/repo',
          'name': 'notes.txt',
          'detected_mime': 'text/plain',
          'size_bytes': 5,
          'sha256': 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
          'expires_at': '2026-07-29T00:00:00Z',
        },
      }),
    ]);
    final item = await api.stageAttachment(
      projectId: '/repo',
      fileName: 'notes.txt',
      bytes: Uint8List.fromList(utf8.encode('hello')),
      mimeType: 'text/plain',
    );
    final request = (dio.httpClientAdapter as _RecordingAdapter).requests.single;
    expect(request.path, '/api/v1/oneshot/attachments');
    expect(request.method, 'POST');
    expect(request.data, isA<FormData>());
    expect(item.id, 'oat_mobile1');
  });

  test('Attachment staging rejects oversize bytes before network', () async {
    final api = apiFor([]);
    await expectLater(
      api.stageAttachment(
        projectId: '/repo',
        fileName: 'too-big.bin',
        bytes: Uint8List(20 * 1024 * 1024 + 1),
      ),
      throwsA(isA<AgentTasksApiException>()),
    );
    expect((dio.httpClientAdapter as _RecordingAdapter).requests, isEmpty);
  });

}
