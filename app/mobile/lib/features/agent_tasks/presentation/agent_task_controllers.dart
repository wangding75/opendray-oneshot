import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'package:opendray/features/agent_tasks/data/agent_tasks_api.dart';
import 'package:opendray/features/agent_tasks/data/agent_tasks_repository.dart';
import 'package:opendray/features/agent_tasks/domain/agent_task_models.dart';

class AgentTaskListState {
  const AgentTaskListState({
    this.items = const [],
    this.nextCursor,
    this.status,
    this.projectId,
    this.loading = false,
    this.loadingMore = false,
    this.offline = false,
    this.error,
  });

  final List<AgentTask> items;
  final String? nextCursor;
  final AgentTaskStatus? status;
  final String? projectId;
  final bool loading;
  final bool loadingMore;
  final bool offline;
  final AgentTasksApiException? error;

  AgentTaskListState copyWith({
    List<AgentTask>? items,
    String? nextCursor,
    bool clearNextCursor = false,
    AgentTaskStatus? status,
    bool clearStatus = false,
    String? projectId,
    bool clearProject = false,
    bool? loading,
    bool? loadingMore,
    bool? offline,
    AgentTasksApiException? error,
    bool clearError = false,
  }) => AgentTaskListState(
    items: items ?? this.items,
    nextCursor: clearNextCursor ? null : nextCursor ?? this.nextCursor,
    status: clearStatus ? null : status ?? this.status,
    projectId: clearProject ? null : projectId ?? this.projectId,
    loading: loading ?? this.loading,
    loadingMore: loadingMore ?? this.loadingMore,
    offline: offline ?? this.offline,
    error: clearError ? null : error ?? this.error,
  );
}

class AgentTaskListController extends StateNotifier<AgentTaskListState> {
  AgentTaskListController(this._repository)
    : super(const AgentTaskListState(loading: true)) {
    unawaited(refresh());
    _subscribe();
  }

  final AgentTasksRepository _repository;
  StreamSubscription<AgentStreamFrame>? _subscription;
  Timer? _refreshDebounce;

  Future<void> refresh() async {
    state = state.copyWith(loading: true, clearError: true);
    try {
      final page = await _repository.listTasks(
        projectId: state.projectId,
        status: state.status,
      );
      state = state.copyWith(
        items: _sort(page.items),
        nextCursor: page.nextCursor,
        clearNextCursor: page.nextCursor == null,
        loading: false,
        offline: false,
        clearError: true,
      );
    } on AgentTasksApiException catch (error) {
      state = state.copyWith(
        loading: false,
        offline: error.isOffline,
        error: error,
      );
    }
  }

  Future<void> loadMore() async {
    final cursor = state.nextCursor;
    if (cursor == null || state.loadingMore) return;
    state = state.copyWith(loadingMore: true, clearError: true);
    try {
      final page = await _repository.listTasks(
        cursor: cursor,
        projectId: state.projectId,
        status: state.status,
      );
      final merged = <String, AgentTask>{
        for (final task in state.items) task.id: task,
        for (final task in page.items) task.id: task,
      };
      state = state.copyWith(
        items: _sort(merged.values.toList()),
        nextCursor: page.nextCursor,
        clearNextCursor: page.nextCursor == null,
        loadingMore: false,
        offline: false,
        clearError: true,
      );
    } on AgentTasksApiException catch (error) {
      state = state.copyWith(
        loadingMore: false,
        offline: error.isOffline,
        error: error,
      );
    }
  }

  Future<void> setStatus(AgentTaskStatus? status) async {
    state = state.copyWith(status: status, clearStatus: status == null);
    await refresh();
  }

  Future<void> setProject(String? projectId) async {
    state = state.copyWith(
      projectId: projectId,
      clearProject: projectId == null || projectId.isEmpty,
    );
    _subscribe();
    await refresh();
  }

  void _subscribe() {
    unawaited(_subscription?.cancel());
    _subscription = _repository
        .taskStream(projectId: state.projectId)
        .listen((_) => _scheduleRefresh(), onError: (_) {});
  }

  void _scheduleRefresh() {
    _refreshDebounce?.cancel();
    _refreshDebounce = Timer(const Duration(milliseconds: 250), refresh);
  }

  List<AgentTask> _sort(List<AgentTask> items) {
    items.sort((a, b) => b.updatedAt.compareTo(a.updatedAt));
    return items;
  }

  @override
  void dispose() {
    _refreshDebounce?.cancel();
    unawaited(_subscription?.cancel());
    super.dispose();
  }
}

final agentTaskProjectOptionsProvider = FutureProvider.autoDispose((ref) {
  return ref.watch(agentTasksRepositoryProvider).listProjects();
});

final agentTaskListControllerProvider =
    StateNotifierProvider.autoDispose<
      AgentTaskListController,
      AgentTaskListState
    >((ref) {
      return AgentTaskListController(ref.watch(agentTasksRepositoryProvider));
    });

class AgentTaskDetailState {
  const AgentTaskDetailState({
    this.task,
    this.runs = const [],
    this.selectedRun,
    this.events = const [],
    this.artifacts = const [],
    this.loading = true,
    this.actionLoading = false,
    this.offline = false,
    this.error,
    this.streamCursor,
  });

  final AgentTask? task;
  final List<AgentRun> runs;
  final AgentRun? selectedRun;
  final List<AgentEvent> events;
  final List<AgentArtifact> artifacts;
  final bool loading;
  final bool actionLoading;
  final bool offline;
  final AgentTasksApiException? error;
  final String? streamCursor;

  AgentTaskDetailState copyWith({
    AgentTask? task,
    List<AgentRun>? runs,
    AgentRun? selectedRun,
    bool clearSelectedRun = false,
    List<AgentEvent>? events,
    List<AgentArtifact>? artifacts,
    bool? loading,
    bool? actionLoading,
    bool? offline,
    AgentTasksApiException? error,
    bool clearError = false,
    String? streamCursor,
    bool clearStreamCursor = false,
  }) => AgentTaskDetailState(
    task: task ?? this.task,
    runs: runs ?? this.runs,
    selectedRun: clearSelectedRun ? null : selectedRun ?? this.selectedRun,
    events: events ?? this.events,
    artifacts: artifacts ?? this.artifacts,
    loading: loading ?? this.loading,
    actionLoading: actionLoading ?? this.actionLoading,
    offline: offline ?? this.offline,
    error: clearError ? null : error ?? this.error,
    streamCursor: clearStreamCursor ? null : streamCursor ?? this.streamCursor,
  );
}

class AgentTaskDetailController extends StateNotifier<AgentTaskDetailState> {
  AgentTaskDetailController(this._repository, this.taskId)
    : super(const AgentTaskDetailState()) {
    unawaited(refresh());
  }

  final AgentTasksRepository _repository;
  final String taskId;
  StreamSubscription<AgentStreamFrame>? _runSubscription;
  Timer? _reloadDebounce;
  String? _subscribedRunId;

  Future<void> refresh() async {
    _stopRunStream();
    state = state.copyWith(
      loading: true,
      clearError: true,
      clearStreamCursor: true,
    );
    try {
      final task = await _repository.getTask(taskId);
      final runPage = await _repository.listRuns(
        taskId,
        projectId: task.projectId,
      );
      final runs = [...runPage.items]
        ..sort((a, b) => b.createdAt.compareTo(a.createdAt));
      final selected = _selectRun(task, runs);
      var events = const <AgentEvent>[];
      var artifacts = const <AgentArtifact>[];
      if (selected != null) {
        final eventItems = await _loadAllEvents(selected.id, task.projectId);
        final artifactItems = await _loadAllArtifacts(
          selected.id,
          task.projectId,
        );
        events = _dedupeEvents(eventItems);
        artifacts = artifactItems;
      }
      state = state.copyWith(
        task: task,
        runs: runs,
        selectedRun: selected,
        clearSelectedRun: selected == null,
        clearStreamCursor: true,
        events: events,
        artifacts: artifacts,
        loading: false,
        offline: false,
        clearError: true,
      );
      _subscribeRun(selected, task.projectId);
    } on AgentTasksApiException catch (error) {
      state = state.copyWith(
        loading: false,
        offline: error.isOffline,
        error: error,
      );
    }
  }

  Future<void> selectRun(AgentRun run) async {
    final task = state.task;
    if (task == null || run.id == state.selectedRun?.id) return;
    _stopRunStream();
    state = state.copyWith(
      selectedRun: run,
      events: const [],
      artifacts: const [],
      loading: true,
      clearError: true,
      clearStreamCursor: true,
    );
    try {
      final eventItems = await _loadAllEvents(run.id, task.projectId);
      final artifactItems = await _loadAllArtifacts(run.id, task.projectId);
      state = state.copyWith(
        events: _dedupeEvents(eventItems),
        artifacts: artifactItems,
        loading: false,
        offline: false,
      );
      _subscribeRun(run, task.projectId);
    } on AgentTasksApiException catch (error) {
      state = state.copyWith(
        loading: false,
        offline: error.isOffline,
        error: error,
      );
    }
  }

  Future<List<AgentEvent>> _loadAllEvents(
    String runId,
    String projectId,
  ) async {
    const maxHistoricalEvents = 10000;
    final events = <AgentEvent>[];
    String? cursor;
    final visitedCursors = <String>{};
    for (;;) {
      final page = await _repository.listEvents(
        runId,
        projectId: projectId,
        cursor: cursor,
      );
      events.addAll(page.items);
      final nextCursor = page.nextCursor;
      if (events.length >= maxHistoricalEvents ||
          nextCursor == null ||
          nextCursor.isEmpty ||
          !visitedCursors.add(nextCursor)) {
        break;
      }
      cursor = nextCursor;
    }
    return events.length <= maxHistoricalEvents
        ? events
        : events.sublist(events.length - maxHistoricalEvents);
  }

  Future<List<AgentArtifact>> _loadAllArtifacts(
    String runId,
    String projectId,
  ) async {
    const maxArtifacts = 1000;
    final artifacts = <AgentArtifact>[];
    final visitedCursors = <String>{};
    String? cursor;
    for (;;) {
      final page = await _repository.listArtifacts(
        runId,
        projectId: projectId,
        cursor: cursor,
      );
      artifacts.addAll(page.items);
      final nextCursor = page.nextCursor;
      if (artifacts.length >= maxArtifacts ||
          nextCursor == null ||
          nextCursor.isEmpty ||
          !visitedCursors.add(nextCursor)) {
        break;
      }
      cursor = nextCursor;
    }
    return artifacts.length <= maxArtifacts
        ? artifacts
        : artifacts.sublist(0, maxArtifacts);
  }

  Future<void> cancel() async {
    final task = state.task;
    if (task == null || state.actionLoading) return;
    await _runAction(
      () => _repository.cancelTask(task.id, projectId: task.projectId),
    );
  }

  Future<void> retry({String promptDelta = ''}) async {
    final task = state.task;
    if (task == null || state.actionLoading) return;
    final key = AgentTasksApi.newIdempotencyKey('retry-${task.id}');
    await _runAction(
      () => _repository.retryTask(
        task.id,
        projectId: task.projectId,
        idempotencyKey: key,
        promptDelta: promptDelta,
      ),
    );
  }

  Future<void> continueTask(String promptDelta) async {
    final task = state.task;
    if (task == null || state.actionLoading) return;
    final key = AgentTasksApi.newIdempotencyKey('continue-${task.id}');
    await _runAction(
      () => _repository.continueTask(
        task.id,
        ContinueAgentTaskInput(
          projectId: task.projectId,
          providerId: task.providerId,
          promptDelta: promptDelta,
        ),
        idempotencyKey: key,
      ),
    );
  }

  Future<ArtifactDownload> downloadArtifact(AgentArtifact artifact) =>
      _repository.downloadArtifact(artifact);

  Future<void> _runAction(Future<AgentTask> Function() action) async {
    state = state.copyWith(actionLoading: true, clearError: true);
    try {
      final task = await action();
      state = state.copyWith(task: task, actionLoading: false, offline: false);
      await refresh();
    } on AgentTasksApiException catch (error) {
      state = state.copyWith(
        actionLoading: false,
        offline: error.isOffline,
        error: error,
      );
      rethrow;
    }
  }

  AgentRun? _selectRun(AgentTask task, List<AgentRun> runs) {
    final currentId = task.currentRunId;
    if (currentId != null) {
      for (final run in runs) {
        if (run.id == currentId) return run;
      }
    }
    return runs.isEmpty ? null : runs.first;
  }

  void _stopRunStream() {
    _subscribedRunId = null;
    unawaited(_runSubscription?.cancel());
    _runSubscription = null;
  }

  void _subscribeRun(AgentRun? run, String projectId) {
    _stopRunStream();
    _subscribedRunId = run?.id;
    if (run == null) return;
    final runId = run.id;
    _runSubscription = _repository
        .runStream(runId, projectId: projectId, cursor: state.streamCursor)
        .listen((frame) => _applyFrame(runId, frame), onError: (_) {});
  }

  void _applyFrame(String runId, AgentStreamFrame frame) {
    if (_subscribedRunId != runId || state.selectedRun?.id != runId) return;
    state = state.copyWith(streamCursor: frame.cursor);
    if (frame.topic == 'oneshot.run.output') {
      final event = AgentEvent.fromJson({
        ...frame.data,
        'occurred_at': frame.occurredAt.toIso8601String(),
      });
      state = state.copyWith(events: _dedupeEvents([...state.events, event]));
      return;
    }
    if (frame.topic.startsWith('oneshot.run.')) {
      _reloadDebounce?.cancel();
      _reloadDebounce = Timer(const Duration(milliseconds: 200), refresh);
    }
  }

  List<AgentEvent> _dedupeEvents(List<AgentEvent> events) =>
      AgentEventTimeline.merge(events);

  @override
  void dispose() {
    _reloadDebounce?.cancel();
    _stopRunStream();
    super.dispose();
  }
}

final agentTaskDetailControllerProvider = StateNotifierProvider.autoDispose
    .family<AgentTaskDetailController, AgentTaskDetailState, String>((ref, id) {
      return AgentTaskDetailController(
        ref.watch(agentTasksRepositoryProvider),
        id,
      );
    });
