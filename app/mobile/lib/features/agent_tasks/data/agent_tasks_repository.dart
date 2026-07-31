import 'dart:async';
import 'dart:typed_data';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'package:opendray/core/api/project_docs_api.dart';
import 'package:opendray/features/agent_tasks/data/agent_tasks_api.dart';
import 'package:opendray/features/agent_tasks/data/agent_tasks_stream.dart';
import 'package:opendray/features/agent_tasks/domain/agent_task_models.dart';

class AgentTasksRepository {
  const AgentTasksRepository({
    required AgentTasksApi api,
    required AgentTaskStreamClient? streams,
  })  : _api = api,
        _streams = streams;

  final AgentTasksApi _api;
  final AgentTaskStreamClient? _streams;

  Future<AgentPage<AgentTask>> listTasks({
    String? cursor,
    String? projectId,
    AgentTaskStatus? status,
  }) => _api.listTasks(
        cursor: cursor,
        projectId: projectId,
        status: status,
      );

  Future<AgentTask> getTask(String taskId, {String? projectId}) =>
      _api.getTask(taskId, projectId: projectId);

  Future<AgentTask> createTask(
    CreateAgentTaskInput input, {
    String? idempotencyKey,
  }) => _api.createTask(
        input,
        idempotencyKey:
            idempotencyKey ?? AgentTasksApi.newIdempotencyKey('create'),
      );

  Future<AgentPage<AgentRun>> listRuns(
    String taskId, {
    String? cursor,
    String? projectId,
  }) => _api.listRuns(
        taskId,
        cursor: cursor,
        projectId: projectId,
      );

  Future<({AgentRun run, AgentTask task})> getRun(
    String runId, {
    String? projectId,
  }) => _api.getRun(runId, projectId: projectId);

  Future<AgentPage<AgentEvent>> listEvents(
    String runId, {
    String? cursor,
    String? projectId,
  }) => _api.listEvents(
        runId,
        cursor: cursor,
        projectId: projectId,
      );

  Future<AgentPage<AgentArtifact>> listArtifacts(
    String runId, {
    String? cursor,
    String? projectId,
  }) => _api.listArtifacts(
        runId,
        cursor: cursor,
        projectId: projectId,
      );

  Future<AgentTask> continueTask(
    String taskId,
    ContinueAgentTaskInput input, {
    String? idempotencyKey,
  }) => _api.continueTask(
        taskId,
        input,
        idempotencyKey:
            idempotencyKey ?? AgentTasksApi.newIdempotencyKey('continue'),
      );

  Future<AgentTask> cancelTask(
    String taskId, {
    required String projectId,
  }) => _api.cancelTask(taskId, projectId: projectId);

  Future<AgentTask> retryTask(
    String taskId, {
    required String projectId,
    String? idempotencyKey,
    String promptDelta = '',
  }) => _api.retryTask(
        taskId,
        projectId: projectId,
        idempotencyKey:
            idempotencyKey ?? AgentTasksApi.newIdempotencyKey('retry'),
        promptDelta: promptDelta,
      );


  Future<StagedAgentAttachment> stageAttachment({
    required String projectId,
    required String fileName,
    required Uint8List bytes,
    String? mimeType,
  }) => _api.stageAttachment(
        projectId: projectId,
        fileName: fileName,
        bytes: bytes,
        mimeType: mimeType,
      );

  Future<void> deleteStagedAttachment(
    String attachmentId, {
    required String projectId,
  }) => _api.deleteStagedAttachment(
        attachmentId,
        projectId: projectId,
      );

  Future<ArtifactDownload> downloadArtifact(AgentArtifact artifact) =>
      _api.downloadArtifact(artifact);

  Future<List<AgentProviderCapability>> listProviderCapabilities() =>
      _api.listProviderCapabilities();

  Future<List<ProjectSummary>> listProjects() => _api.listProjects();

  Stream<AgentStreamFrame> taskStream({
    String? projectId,
    String? cursor,
  }) => _streams?.taskStream(projectId: projectId, cursor: cursor) ??
      const Stream<AgentStreamFrame>.empty();

  Stream<AgentStreamFrame> runStream(
    String runId, {
    String? projectId,
    String? cursor,
  }) => _streams?.runStream(
        runId,
        projectId: projectId,
        cursor: cursor,
      ) ??
      const Stream<AgentStreamFrame>.empty();
}

final agentTasksRepositoryProvider = Provider<AgentTasksRepository>((ref) {
  return AgentTasksRepository(
    api: ref.watch(agentTasksApiProvider),
    streams: ref.watch(agentTaskStreamClientProvider),
  );
});
