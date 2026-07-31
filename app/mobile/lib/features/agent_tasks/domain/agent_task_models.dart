import 'dart:convert';
import 'dart:typed_data';

/// One-shot mobile models intentionally live outside the PTY Session model
/// tree. The gateway JSON contract is the source of truth.
enum AgentTaskStatus {
  pending,
  queued,
  running,
  waitingInput,
  completed,
  failed,
  cancelled,
  timedOut,
  unknown;

  static AgentTaskStatus parse(Object? value) => switch (value?.toString()) {
        'pending' => AgentTaskStatus.pending,
        'queued' => AgentTaskStatus.queued,
        'running' => AgentTaskStatus.running,
        'waiting_input' => AgentTaskStatus.waitingInput,
        'completed' => AgentTaskStatus.completed,
        'failed' => AgentTaskStatus.failed,
        'cancelled' => AgentTaskStatus.cancelled,
        'timed_out' => AgentTaskStatus.timedOut,
        _ => AgentTaskStatus.unknown,
      };

  String get wire => switch (this) {
        AgentTaskStatus.pending => 'pending',
        AgentTaskStatus.queued => 'queued',
        AgentTaskStatus.running => 'running',
        AgentTaskStatus.waitingInput => 'waiting_input',
        AgentTaskStatus.completed => 'completed',
        AgentTaskStatus.failed => 'failed',
        AgentTaskStatus.cancelled => 'cancelled',
        AgentTaskStatus.timedOut => 'timed_out',
        AgentTaskStatus.unknown => 'unknown',
      };

  bool get isActive => switch (this) {
        AgentTaskStatus.pending ||
        AgentTaskStatus.queued ||
        AgentTaskStatus.running => true,
        _ => false,
      };

  bool get canContinue => this == AgentTaskStatus.waitingInput ||
      this == AgentTaskStatus.completed ||
      this == AgentTaskStatus.failed ||
      this == AgentTaskStatus.timedOut;

  bool get canRetry =>
      this == AgentTaskStatus.failed || this == AgentTaskStatus.timedOut;

  bool get canCancel => isActive || this == AgentTaskStatus.waitingInput;
}

enum AgentRunStatus {
  created,
  starting,
  running,
  collectingOutput,
  waitingInput,
  completed,
  failed,
  cancelled,
  timedOut,
  unknown;

  static AgentRunStatus parse(Object? value) => switch (value?.toString()) {
        'created' => AgentRunStatus.created,
        'starting' => AgentRunStatus.starting,
        'running' => AgentRunStatus.running,
        'collecting_output' => AgentRunStatus.collectingOutput,
        'waiting_input' => AgentRunStatus.waitingInput,
        'completed' => AgentRunStatus.completed,
        'failed' => AgentRunStatus.failed,
        'cancelled' => AgentRunStatus.cancelled,
        'timed_out' => AgentRunStatus.timedOut,
        _ => AgentRunStatus.unknown,
      };

  String get wire => switch (this) {
        AgentRunStatus.created => 'created',
        AgentRunStatus.starting => 'starting',
        AgentRunStatus.running => 'running',
        AgentRunStatus.collectingOutput => 'collecting_output',
        AgentRunStatus.waitingInput => 'waiting_input',
        AgentRunStatus.completed => 'completed',
        AgentRunStatus.failed => 'failed',
        AgentRunStatus.cancelled => 'cancelled',
        AgentRunStatus.timedOut => 'timed_out',
        AgentRunStatus.unknown => 'unknown',
      };
}

class AgentTaskSource {
  const AgentTaskSource({
    required this.kind,
    this.channelId,
    this.conversationId,
    this.threadId,
    this.messageId,
    this.clientRequestId,
  });

  factory AgentTaskSource.fromJson(Map<String, dynamic> json) {
    final replyAddress = _map(json['reply_address']);
    return AgentTaskSource(
      kind: json['kind']?.toString() ?? '',
      channelId: _nullableString(
        json['channel_id'] ?? replyAddress['channel_id'],
      ),
      conversationId: _nullableString(replyAddress['conversation_id']),
      threadId: _nullableString(replyAddress['thread_id']),
      messageId: _nullableString(
        json['source_message_id'] ?? replyAddress['message_id'],
      ),
      clientRequestId: _nullableString(json['client_request_id']),
    );
  }

  final String kind;
  final String? channelId;
  final String? conversationId;
  final String? threadId;
  final String? messageId;
  final String? clientRequestId;

  Map<String, dynamic> toJson() => {
        'kind': kind,
        if (channelId != null) 'channel_id': channelId,
        if (messageId != null) 'source_message_id': messageId,
        if (clientRequestId != null) 'client_request_id': clientRequestId,
        if (conversationId != null || threadId != null || messageId != null)
          'reply_address': {
            if (channelId != null) 'channel_id': channelId,
            if (conversationId != null) 'conversation_id': conversationId,
            if (threadId != null) 'thread_id': threadId,
            if (messageId != null) 'message_id': messageId,
          },
      };
}

class AgentTask {
  const AgentTask({
    required this.id,
    required this.projectId,
    required this.providerId,
    required this.source,
    required this.prompt,
    required this.status,
    required this.version,
    required this.createdAt,
    required this.updatedAt,
    this.currentRunId,
    this.runtimeContextId,
  });

  factory AgentTask.fromJson(Map<String, dynamic> json) => AgentTask(
        id: json['id']?.toString() ?? '',
        projectId: json['project_id']?.toString() ?? '',
        providerId: json['provider_id']?.toString() ?? '',
        source: AgentTaskSource.fromJson(_map(json['source'])),
        prompt: json['prompt']?.toString() ?? '',
        status: AgentTaskStatus.parse(json['status']),
        currentRunId: _nullableString(json['current_run_id']),
        runtimeContextId: _nullableString(json['runtime_context_id']),
        version: _int(json['version']),
        createdAt: _date(json['created_at']),
        updatedAt: _date(json['updated_at']),
      );

  final String id;
  final String projectId;
  final String providerId;
  final AgentTaskSource source;
  final String prompt;
  final AgentTaskStatus status;
  final String? currentRunId;
  final String? runtimeContextId;
  final int version;
  final DateTime createdAt;
  final DateTime updatedAt;
}

class AgentRun {
  const AgentRun({
    required this.id,
    required this.taskId,
    required this.deliveryId,
    required this.providerId,
    required this.status,
    required this.createdAt,
    this.runtimeContextId,
    this.pid,
    this.exitCode,
    this.errorCode,
    this.errorMessage,
    this.startedAt,
    this.finishedAt,
  });

  factory AgentRun.fromJson(Map<String, dynamic> json) => AgentRun(
        id: json['id']?.toString() ?? '',
        taskId: json['task_id']?.toString() ?? '',
        deliveryId: json['delivery_id']?.toString() ?? '',
        providerId: json['provider_id']?.toString() ?? '',
        runtimeContextId: _nullableString(json['runtime_context_id']),
        status: AgentRunStatus.parse(json['status']),
        pid: _nullableInt(json['pid']),
        exitCode: _nullableInt(json['exit_code']),
        errorCode: _nullableString(json['error_code']),
        errorMessage: _nullableString(json['error_message']),
        startedAt: _nullableDate(json['started_at']),
        finishedAt: _nullableDate(json['finished_at']),
        createdAt: _date(json['created_at']),
      );

  final String id;
  final String taskId;
  final String deliveryId;
  final String providerId;
  final String? runtimeContextId;
  final AgentRunStatus status;
  final int? pid;
  final int? exitCode;
  final String? errorCode;
  final String? errorMessage;
  final DateTime? startedAt;
  final DateTime? finishedAt;
  final DateTime createdAt;
}

class AgentEvent {
  const AgentEvent({
    required this.id,
    required this.runId,
    required this.sequence,
    required this.type,
    required this.content,
    required this.occurredAt,
    this.adapterId = '',
    this.adapterVersion = '',
  });

  factory AgentEvent.fromJson(Map<String, dynamic> json) => AgentEvent(
        id: json['id']?.toString() ?? json['event_id']?.toString() ?? '',
        runId: json['run_id']?.toString() ?? '',
        sequence: _int(json['sequence']),
        type: json['type']?.toString() ??
            json['event_type']?.toString() ??
            'unknown',
        adapterId: json['adapter_id']?.toString() ?? '',
        adapterVersion: json['adapter_version']?.toString() ?? '',
        content: _map(json['content']),
        occurredAt: _date(json['occurred_at'] ?? json['ts']),
      );

  final String id;
  final String runId;
  final int sequence;
  final String type;
  final String adapterId;
  final String adapterVersion;
  final Map<String, dynamic> content;
  final DateTime occurredAt;

  String get streamName => switch (content['stream']?.toString()) {
        'stderr' => 'stderr',
        'stdout' => 'stdout',
        _ => type.contains('stderr')
            ? 'stderr'
            : type.contains('stdout')
                ? 'stdout'
                : 'raw',
      };

  String get displayText {
    for (final key in const ['text', 'message', 'delta', 'output']) {
      final value = content[key];
      if (value is String && value.isNotEmpty) return value;
    }
    return const JsonEncoder.withIndent('  ').convert(content);
  }
}


class AgentEventTimeline {
  const AgentEventTimeline._();

  static List<AgentEvent> merge(
    Iterable<AgentEvent> events, {
    int maxEvents = 10000,
  }) {
    final byKey = <String, AgentEvent>{};
    for (final event in events) {
      final key = event.id.isNotEmpty
          ? event.id
          : '${event.sequence}|${event.type}|${event.occurredAt.toIso8601String()}';
      byKey[key] = event;
    }
    final result = byKey.values.toList()
      ..sort((a, b) {
        final sequence = a.sequence.compareTo(b.sequence);
        return sequence != 0
            ? sequence
            : a.occurredAt.compareTo(b.occurredAt);
      });
    if (result.length <= maxEvents) return result;
    return result.sublist(result.length - maxEvents);
  }
}

class AgentArtifact {
  const AgentArtifact({
    required this.id,
    required this.taskId,
    required this.kind,
    required this.name,
    required this.contentType,
    required this.sizeBytes,
    required this.sha256,
    required this.createdAt,
    this.runId,
    this.metadata = const {},
  });

  factory AgentArtifact.fromJson(Map<String, dynamic> json) => AgentArtifact(
        id: json['id']?.toString() ?? '',
        taskId: json['task_id']?.toString() ?? '',
        runId: _nullableString(json['run_id']),
        kind: json['kind']?.toString() ?? '',
        name: json['name']?.toString() ?? 'artifact.bin',
        contentType: json['content_type']?.toString() ??
            'application/octet-stream',
        sizeBytes: _int(json['size_bytes']),
        sha256: json['sha256']?.toString() ?? '',
        metadata: _map(json['metadata']),
        createdAt: _date(json['created_at']),
      );

  final String id;
  final String taskId;
  final String? runId;
  final String kind;
  final String name;
  final String contentType;
  final int sizeBytes;
  final String sha256;
  final Map<String, dynamic> metadata;
  final DateTime createdAt;
}

class AgentProviderCapability {
  const AgentProviderCapability({
    required this.providerId,
    required this.displayName,
    required this.enabled,
    required this.supportsNonInteractive,
    required this.supportsResume,
    required this.structuredOutput,
    required this.attachments,
    required this.cancellation,
  });

  factory AgentProviderCapability.fromProviderGatewayJson(
    Map<String, dynamic> json,
  ) {
    final manifest = _map(json['manifest']);
    final descriptor = _map(json['oneshot']);
    final oneShotCapabilities = _map(descriptor['capabilities']);
    final catalogProviderId = manifest['id']?.toString() ?? '';
    final providerId = descriptor['provider_id']?.toString().isNotEmpty == true
        ? descriptor['provider_id'].toString()
        : catalogProviderId == 'claude'
            ? 'claude-code'
            : catalogProviderId;
    final hasDescriptor = descriptor.isNotEmpty;
    final supportedFallback =
        catalogProviderId == 'codex' || catalogProviderId == 'claude';
    return AgentProviderCapability(
      providerId: providerId,
      displayName: descriptor['display_name']?.toString() ??
          manifest['displayName']?.toString() ??
          manifest['displayName_zh']?.toString() ??
          catalogProviderId,
      enabled: hasDescriptor
          ? descriptor['enabled'] == true
          : json['enabled'] == true && supportedFallback,
      supportsNonInteractive: hasDescriptor
          ? oneShotCapabilities['supports_non_interactive'] == true
          : supportedFallback,
      supportsResume: hasDescriptor
          ? oneShotCapabilities['supports_resume'] == true
          : supportedFallback,
      structuredOutput: hasDescriptor
          ? oneShotCapabilities['structured_output'] == true
          : supportedFallback,
      // PTY manifest supportsImages is not a One-shot attachment contract.
      attachments: hasDescriptor
          ? oneShotCapabilities['attachments'] == true
          : false,
      cancellation: hasDescriptor
          ? oneShotCapabilities['cancellation'] == true
          : supportedFallback,
    );
  }

  final String providerId;
  final String displayName;
  final bool enabled;
  final bool supportsNonInteractive;
  final bool supportsResume;
  final bool structuredOutput;
  final bool attachments;
  final bool cancellation;
}

class AgentPage<T> {
  const AgentPage({required this.items, this.nextCursor});

  final List<T> items;
  final String? nextCursor;
}

class AgentStreamFrame {
  const AgentStreamFrame({
    required this.topic,
    required this.occurredAt,
    required this.cursor,
    required this.data,
  });

  factory AgentStreamFrame.fromJson(Map<String, dynamic> json) =>
      AgentStreamFrame(
        topic: json['topic']?.toString() ?? '',
        occurredAt: _date(json['ts']),
        cursor: json['cursor']?.toString() ?? '',
        data: _map(json['data']),
      );

  final String topic;
  final DateTime occurredAt;
  final String cursor;
  final Map<String, dynamic> data;

  String get identity {
    final id = data['event_id'] ?? data['id'] ?? data['run_id'] ?? data['task_id'];
    return '$topic|${id ?? ''}|$cursor';
  }
}

class CreateAgentTaskInput {
  const CreateAgentTaskInput({
    required this.projectId,
    required this.providerId,
    required this.prompt,
    required this.workspacePath,
    this.attachmentRefs = const [],
    this.timeoutSeconds = 3600,
    this.telegramNotify = false,
    this.continueContext = false,
  });

  final String projectId;
  final String providerId;
  final String prompt;
  final String workspacePath;
  final List<String> attachmentRefs;
  final int timeoutSeconds;
  final bool telegramNotify;
  final bool continueContext;

  Map<String, dynamic> toJson(String clientRequestId) => {
        'project_id': projectId,
        'provider_id': providerId,
        'prompt': prompt,
        'workspace_path': workspacePath,
        'attachment_refs': attachmentRefs,
        'timeout_seconds': timeoutSeconds,
        'source': {
          'kind': 'mobile',
          'client_request_id': clientRequestId,
        },
        'options': {
          'telegram_notify': telegramNotify,
          'context_mode': continueContext ? 'continue' : 'new',
        },
      };
}

class ContinueAgentTaskInput {
  const ContinueAgentTaskInput({
    required this.projectId,
    required this.promptDelta,
    this.providerId = '',
    this.workspacePath = '',
    this.attachmentRefs = const [],
  });

  final String projectId;
  final String providerId;
  final String workspacePath;
  final String promptDelta;
  final List<String> attachmentRefs;

  Map<String, dynamic> toJson() => {
        'project_id': projectId,
        if (providerId.isNotEmpty) 'provider_id': providerId,
        if (workspacePath.isNotEmpty) 'workspace_path': workspacePath,
        'prompt_delta': promptDelta,
        'attachment_refs': attachmentRefs,
      };
}

class ArtifactDownload {
  const ArtifactDownload({
    required this.bytes,
    required this.contentType,
    required this.fileName,
    required this.sha256,
    required this.integrityVerified,
  });

  final Uint8List bytes;
  final String contentType;
  final String fileName;
  final String sha256;
  final bool integrityVerified;
}

Map<String, dynamic> _map(Object? value) =>
    value is Map ? Map<String, dynamic>.from(value) : <String, dynamic>{};

String? _nullableString(Object? value) {
  final result = value?.toString();
  return result == null || result.isEmpty ? null : result;
}

int _int(Object? value) => value is num ? value.toInt() : int.tryParse('$value') ?? 0;
int? _nullableInt(Object? value) => value == null ? null : _int(value);
DateTime _date(Object? value) =>
    DateTime.tryParse(value?.toString() ?? '')?.toUtc() ??
    DateTime.fromMillisecondsSinceEpoch(0, isUtc: true);
DateTime? _nullableDate(Object? value) => value == null ? null : _date(value);

class StagedAgentAttachment {
  const StagedAgentAttachment({
    required this.id,
    required this.projectId,
    required this.name,
    required this.detectedMime,
    required this.sizeBytes,
    required this.sha256,
    required this.expiresAt,
  });

  factory StagedAgentAttachment.fromJson(Map<String, dynamic> json) {
    return StagedAgentAttachment(
      id: json['id']?.toString() ?? '',
      projectId: json['project_id']?.toString() ?? '',
      name: json['name']?.toString() ?? '',
      detectedMime: json['detected_mime']?.toString() ?? '',
      sizeBytes: (json['size_bytes'] as num?)?.toInt() ?? 0,
      sha256: json['sha256']?.toString() ?? '',
      expiresAt: DateTime.tryParse(json['expires_at']?.toString() ?? '')?.toUtc(),
    );
  }

  final String id;
  final String projectId;
  final String name;
  final String detectedMime;
  final int sizeBytes;
  final String sha256;
  final DateTime? expiresAt;
}
