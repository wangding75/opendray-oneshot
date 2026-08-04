import 'dart:convert';
import 'dart:math';
import 'dart:typed_data';

import 'package:crypto/crypto.dart';
import 'package:dio/dio.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'package:opendray/core/api/dio_provider.dart';
import 'package:opendray/core/api/project_docs_api.dart';
import 'package:opendray/features/agent_tasks/domain/agent_task_models.dart';

class AgentTasksApiException implements Exception {
  const AgentTasksApiException({
    required this.statusCode,
    required this.code,
    required this.message,
    required this.retryable,
    this.requestId,
    this.details = const {},
  });

  factory AgentTasksApiException.from(Object error) {
    final api = toApiException(error);
    final body = api.body;
    final envelope = body is Map ? body['error'] : null;
    if (envelope is Map) {
      return AgentTasksApiException(
        statusCode: api.statusCode,
        code: envelope['code']?.toString() ?? 'oneshot.unknown',
        message: envelope['message']?.toString() ?? api.message,
        retryable: envelope['retryable'] == true,
        requestId: envelope['request_id']?.toString(),
        details: envelope['details'] is Map
            ? Map<String, dynamic>.from(envelope['details'] as Map)
            : const {},
      );
    }
    return AgentTasksApiException(
      statusCode: api.statusCode,
      code: api.statusCode == 0 ? 'network.unavailable' : 'http.error',
      message: api.message,
      retryable: api.statusCode == 0 || api.statusCode >= 500,
    );
  }

  final int statusCode;
  final String code;
  final String message;
  final bool retryable;
  final String? requestId;
  final Map<String, dynamic> details;

  bool get isUnauthorized => statusCode == 401;
  bool get isForbidden => statusCode == 403;
  bool get isOffline => statusCode == 0;

  @override
  String toString() => '$code: $message';
}

class AgentTasksApi {
  AgentTasksApi(this._dio);

  final Dio _dio;

  Future<AgentPage<AgentTask>> listTasks({
    String? cursor,
    int limit = 50,
    String? projectId,
    AgentTaskStatus? status,
  }) async {
    try {
      final response = await _dio.get<Map<String, dynamic>>(
        '/api/v1/oneshot/tasks',
        queryParameters: {
          'limit': limit,
          if (cursor != null && cursor.isNotEmpty) 'cursor': cursor,
          if (projectId != null && projectId.isNotEmpty)
            'project_id': projectId,
          if (status != null && status != AgentTaskStatus.unknown)
            'status': status.wire,
        },
      );
      return _page(response.data, AgentTask.fromJson);
    } on Object catch (error) {
      throw AgentTasksApiException.from(error);
    }
  }

  Future<AgentTask> getTask(String taskId, {String? projectId}) async {
    try {
      final response = await _dio.get<Map<String, dynamic>>(
        '/api/v1/oneshot/tasks/$taskId',
        queryParameters: {
          if (projectId != null && projectId.isNotEmpty)
            'project_id': projectId,
        },
      );
      return AgentTask.fromJson(response.data ?? const {});
    } on Object catch (error) {
      throw AgentTasksApiException.from(error);
    }
  }

  Future<AgentTask> createTask(
    CreateAgentTaskInput input, {
    required String idempotencyKey,
  }) async {
    try {
      final response = await _dio.post<Map<String, dynamic>>(
        '/api/v1/oneshot/tasks',
        data: input.toJson(idempotencyKey),
        options: Options(headers: {'Idempotency-Key': idempotencyKey}),
      );
      return AgentTask.fromJson(_map(response.data?['task']));
    } on Object catch (error) {
      throw AgentTasksApiException.from(error);
    }
  }

  Future<AgentPage<AgentRun>> listRuns(
    String taskId, {
    String? cursor,
    int limit = 50,
    String? projectId,
  }) async {
    try {
      final response = await _dio.get<Map<String, dynamic>>(
        '/api/v1/oneshot/tasks/$taskId/runs',
        queryParameters: {
          'limit': limit,
          if (cursor != null && cursor.isNotEmpty) 'cursor': cursor,
          if (projectId != null && projectId.isNotEmpty)
            'project_id': projectId,
        },
      );
      return _page(response.data, AgentRun.fromJson);
    } on Object catch (error) {
      throw AgentTasksApiException.from(error);
    }
  }

  Future<({AgentRun run, AgentTask task})> getRun(
    String runId, {
    String? projectId,
  }) async {
    try {
      final response = await _dio.get<Map<String, dynamic>>(
        '/api/v1/oneshot/runs/$runId',
        queryParameters: {
          if (projectId != null && projectId.isNotEmpty)
            'project_id': projectId,
        },
      );
      final data = response.data ?? const <String, dynamic>{};
      return (
        run: AgentRun.fromJson(_map(data['run'])),
        task: AgentTask.fromJson(_map(data['task'])),
      );
    } on Object catch (error) {
      throw AgentTasksApiException.from(error);
    }
  }

  Future<AgentPage<AgentEvent>> listEvents(
    String runId, {
    String? cursor,
    int limit = 200,
    String? projectId,
  }) async {
    try {
      final response = await _dio.get<Map<String, dynamic>>(
        '/api/v1/oneshot/runs/$runId/events',
        queryParameters: {
          'limit': limit,
          if (cursor != null && cursor.isNotEmpty) 'cursor': cursor,
          if (projectId != null && projectId.isNotEmpty)
            'project_id': projectId,
        },
      );
      return _page(response.data, AgentEvent.fromJson);
    } on Object catch (error) {
      throw AgentTasksApiException.from(error);
    }
  }

  Future<AgentPage<AgentArtifact>> listArtifacts(
    String runId, {
    String? cursor,
    int limit = 100,
    String? projectId,
  }) async {
    try {
      final response = await _dio.get<Map<String, dynamic>>(
        '/api/v1/oneshot/runs/$runId/artifacts',
        queryParameters: {
          'limit': limit,
          if (cursor != null && cursor.isNotEmpty) 'cursor': cursor,
          if (projectId != null && projectId.isNotEmpty)
            'project_id': projectId,
        },
      );
      return _page(response.data, AgentArtifact.fromJson);
    } on Object catch (error) {
      throw AgentTasksApiException.from(error);
    }
  }

  Future<AgentTask> continueTask(
    String taskId,
    ContinueAgentTaskInput input, {
    required String idempotencyKey,
  }) async {
    try {
      final response = await _dio.post<Map<String, dynamic>>(
        '/api/v1/oneshot/tasks/$taskId/continue',
        data: input.toJson(),
        options: Options(headers: {'Idempotency-Key': idempotencyKey}),
      );
      final data = response.data ?? const <String, dynamic>{};
      return AgentTask.fromJson(_map(data['task']));
    } on Object catch (error) {
      throw AgentTasksApiException.from(error);
    }
  }

  Future<AgentTask> cancelTask(
    String taskId, {
    required String projectId,
  }) async {
    try {
      final response = await _dio.post<Map<String, dynamic>>(
        '/api/v1/oneshot/tasks/$taskId/cancel',
        data: {'project_id': projectId},
      );
      return AgentTask.fromJson(_map(response.data?['task']));
    } on Object catch (error) {
      throw AgentTasksApiException.from(error);
    }
  }

  Future<AgentTask> retryTask(
    String taskId, {
    required String projectId,
    required String idempotencyKey,
    String promptDelta = '',
  }) async {
    try {
      final response = await _dio.post<Map<String, dynamic>>(
        '/api/v1/oneshot/tasks/$taskId/retry',
        data: {
          'project_id': projectId,
          if (promptDelta.isNotEmpty) 'prompt_delta': promptDelta,
          'attachment_refs': const <String>[],
        },
        options: Options(headers: {'Idempotency-Key': idempotencyKey}),
      );
      return AgentTask.fromJson(_map(response.data?['task']));
    } on Object catch (error) {
      throw AgentTasksApiException.from(error);
    }
  }

  Future<StagedAgentAttachment> stageAttachment({
    required String projectId,
    required String fileName,
    required Uint8List bytes,
    String? mimeType,
  }) async {
    if (bytes.isEmpty || bytes.length > 20 * 1024 * 1024) {
      throw const AgentTasksApiException(
        statusCode: 400,
        code: 'oneshot.invalid_attachment',
        message: 'Attachment must be between 1 byte and 20 MiB',
        retryable: false,
      );
    }
    try {
      final form = FormData.fromMap({
        'project_id': projectId,
        'source_kind': 'mobile',
        'source_ref': newIdempotencyKey('mobile-attachment'),
        'file': MultipartFile.fromBytes(
          bytes,
          filename: fileName,
          contentType: mimeType == null || mimeType.isEmpty
              ? null
              : DioMediaType.parse(mimeType),
        ),
      });
      final response = await _dio.post<Map<String, dynamic>>(
        '/api/v1/oneshot/attachments',
        data: form,
      );
      return StagedAgentAttachment.fromJson(_map(response.data?['attachment']));
    } on AgentTasksApiException {
      rethrow;
    } on Object catch (error) {
      throw AgentTasksApiException.from(error);
    }
  }

  Future<void> deleteStagedAttachment(
    String attachmentId, {
    required String projectId,
  }) async {
    try {
      await _dio.delete<void>(
        '/api/v1/oneshot/attachments/$attachmentId',
        queryParameters: {'project_id': projectId},
      );
    } on Object catch (error) {
      throw AgentTasksApiException.from(error);
    }
  }

  Future<ArtifactDownload> downloadArtifact(AgentArtifact artifact) async {
    try {
      final response = await _dio.get<List<int>>(
        '/api/v1/oneshot/artifacts/${artifact.id}',
        options: Options(responseType: ResponseType.bytes),
      );
      final bytes = Uint8List.fromList(response.data ?? const <int>[]);
      final digest = sha256.convert(bytes);
      final actual = digest.toString();
      final actualBase64 = base64Encode(digest.bytes);
      final headerLength = int.tryParse(
        response.headers.value(Headers.contentLengthHeader) ?? '',
      );
      final responseDigest = response.headers.value('digest')?.trim();
      final responseETag = response.headers
          .value('etag')
          ?.replaceAll('"', '')
          .trim();
      final lengthMatches =
          headerLength == null || headerLength == bytes.length;
      final metadataLengthMatches =
          artifact.sizeBytes <= 0 || artifact.sizeBytes == bytes.length;
      final metadataHashMatches =
          artifact.sha256.isNotEmpty && actual == artifact.sha256;
      final digestMatches =
          responseDigest == null ||
          responseDigest.isEmpty ||
          responseDigest == 'sha-256=$actualBase64';
      final etagMatches =
          responseETag == null ||
          responseETag.isEmpty ||
          responseETag == actual;
      if (!lengthMatches ||
          !metadataLengthMatches ||
          !metadataHashMatches ||
          !digestMatches ||
          !etagMatches) {
        throw const AgentTasksApiException(
          statusCode: 0,
          code: 'oneshot.artifact_integrity_failed',
          message: 'Downloaded artifact failed integrity verification',
          retryable: true,
        );
      }
      return ArtifactDownload(
        bytes: bytes,
        contentType:
            response.headers.value(Headers.contentTypeHeader) ??
            artifact.contentType,
        fileName: artifact.name,
        sha256: actual,
        integrityVerified: true,
      );
    } on AgentTasksApiException {
      rethrow;
    } on Object catch (error) {
      throw AgentTasksApiException.from(error);
    }
  }

  /// Uses the existing provider catalog endpoint. It exposes the same manifest
  /// capability source used by the One-shot registry while keeping the frozen
  /// /oneshot route set unchanged.
  Future<List<AgentProviderCapability>> listProviderCapabilities() async {
    try {
      final response = await _dio.get<Map<String, dynamic>>(
        '/api/v1/providers',
      );
      final raw = response.data?['providers'];
      if (raw is! List) return const [];
      return raw
          .whereType<Map<String, dynamic>>()
          .map(AgentProviderCapability.fromProviderGatewayJson)
          .where((item) => item.enabled && item.supportsNonInteractive)
          .toList(growable: false);
    } on Object catch (error) {
      throw AgentTasksApiException.from(error);
    }
  }

  Future<List<ProjectSummary>> listProjects() async {
    try {
      final response = await _dio.get<Map<String, dynamic>>(
        '/api/v1/project-docs/projects',
      );
      final raw = response.data?['projects'];
      if (raw is! List) return const [];
      return raw
          .whereType<Map<String, dynamic>>()
          .map(ProjectSummary.fromJson)
          .where((project) => project.status == 'active')
          .toList(growable: false);
    } on Object catch (error) {
      throw AgentTasksApiException.from(error);
    }
  }

  static String newIdempotencyKey([String prefix = 'mobile']) {
    final random = Random.secure();
    final bytes = List<int>.generate(18, (_) => random.nextInt(256));
    return '$prefix-${DateTime.now().microsecondsSinceEpoch}-'
        '${base64UrlEncode(bytes).replaceAll('=', '')}';
  }
}

AgentPage<T> _page<T>(
  Map<String, dynamic>? json,
  T Function(Map<String, dynamic>) parser,
) {
  final raw = json?['items'] ?? json?['Items'];
  final items = raw is List
      ? raw
            .whereType<Map<String, dynamic>>()
            .map(parser)
            .toList(growable: false)
      : <T>[];
  final next = (json?['next_cursor'] ?? json?['NextCursor'])?.toString();
  return AgentPage<T>(
    items: items,
    nextCursor: next == null || next.isEmpty ? null : next,
  );
}

Map<String, dynamic> _map(Object? value) =>
    value is Map ? Map<String, dynamic>.from(value) : <String, dynamic>{};

final agentTasksApiProvider = Provider<AgentTasksApi>((ref) {
  return AgentTasksApi(ref.watch(dioProvider));
});
