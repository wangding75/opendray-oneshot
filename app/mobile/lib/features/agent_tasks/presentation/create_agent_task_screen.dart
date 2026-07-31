import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:file_picker/file_picker.dart';
import 'package:go_router/go_router.dart';

import 'package:opendray/core/api/project_docs_api.dart';
import 'package:opendray/features/agent_tasks/data/agent_tasks_api.dart';
import 'package:opendray/features/agent_tasks/data/agent_tasks_repository.dart';
import 'package:opendray/features/agent_tasks/domain/agent_task_models.dart';
import 'package:opendray/features/agent_tasks/presentation/agent_tasks_strings.dart';

class CreateAgentTaskScreen extends ConsumerStatefulWidget {
  const CreateAgentTaskScreen({super.key});

  @override
  ConsumerState<CreateAgentTaskScreen> createState() =>
      _CreateAgentTaskScreenState();
}

class _CreateAgentTaskScreenState extends ConsumerState<CreateAgentTaskScreen> {
  final _formKey = GlobalKey<FormState>();
  final _promptController = TextEditingController();
  final _workspaceController = TextEditingController();
  final _timeoutController = TextEditingController(text: '3600');
  final List<StagedAgentAttachment> _attachments = [];
  bool _uploadingAttachment = false;
  late Future<
    ({
      List<ProjectSummary> projects,
      List<AgentProviderCapability> providers,
      List<AgentTask> resumableTasks,
    })
  >
  _options;

  String? _projectId;
  String? _providerId;
  bool _continueContext = false;
  String? _resumeTaskId;
  bool _telegramNotify = false;
  bool _submitting = false;
  String? _idempotencyKey;
  AgentTasksApiException? _error;

  @override
  void initState() {
    super.initState();
    _options = _loadOptions();
    for (final controller in [
      _promptController,
      _workspaceController,
      _timeoutController,
    ]) {
      controller.addListener(_invalidateIdempotencyKey);
    }
  }

  Future<
    ({
      List<ProjectSummary> projects,
      List<AgentProviderCapability> providers,
      List<AgentTask> resumableTasks,
    })
  >
  _loadOptions() async {
    final repository = ref.read(agentTasksRepositoryProvider);
    final values = await Future.wait<Object>([
      repository.listProjects(),
      repository.listProviderCapabilities(),
      _loadResumableTasks(repository),
    ]);
    final projects = values[0] as List<ProjectSummary>;
    final providers = values[1] as List<AgentProviderCapability>;
    final resumableTasks = values[2] as List<AgentTask>;
    if (mounted) {
      if (projects.isNotEmpty) {
        _projectId ??= projects.first.cwd;
        _workspaceController.text = projects.first.cwd;
      }
      if (providers.isNotEmpty) _providerId ??= providers.first.providerId;
    }
    return (
      projects: projects,
      providers: providers,
      resumableTasks: resumableTasks,
    );
  }

  Future<List<AgentTask>> _loadResumableTasks(
    AgentTasksRepository repository,
  ) async {
    const maxCandidates = 500;
    const maxScannedTasks = 2000;
    final tasks = <String, AgentTask>{};
    var scannedTasks = 0;
    final visitedCursors = <String>{};
    String? cursor;
    do {
      final page = await repository.listTasks(cursor: cursor);
      scannedTasks += page.items.length;
      for (final task in page.items) {
        if (task.runtimeContextId != null && task.status.canContinue) {
          tasks[task.id] = task;
        }
      }
      final nextCursor = page.nextCursor;
      if (tasks.length >= maxCandidates ||
          scannedTasks >= maxScannedTasks ||
          nextCursor == null ||
          nextCursor.isEmpty ||
          !visitedCursors.add(nextCursor)) {
        break;
      }
      cursor = nextCursor;
    } while (true);
    final values = tasks.values.toList()
      ..sort((a, b) => b.updatedAt.compareTo(a.updatedAt));
    return values.length <= maxCandidates
        ? values
        : values.sublist(0, maxCandidates);
  }

  void _invalidateIdempotencyKey() {
    if (!_submitting) _idempotencyKey = null;
  }

  AgentProviderCapability? _selectedProvider(
    List<AgentProviderCapability> providers,
  ) {
    for (final provider in providers) {
      if (provider.providerId == _providerId) return provider;
    }
    return providers.isEmpty ? null : providers.first;
  }

  List<AgentTask> _compatibleResumeTasks(
    List<AgentTask> tasks,
    AgentProviderCapability provider,
  ) => tasks
      .where(
        (task) =>
            task.providerId == provider.providerId &&
            (_projectId == null || task.projectId == _projectId),
      )
      .toList(growable: false);

  Future<void> _submit(List<AgentProviderCapability> providers) async {
    if (_submitting || !_formKey.currentState!.validate()) return;
    final provider = _selectedProvider(providers);
    if (provider == null || _projectId == null) return;
    final key = _idempotencyKey ??= AgentTasksApi.newIdempotencyKey(
      'mobile-create',
    );
    final attachmentRefs = provider.attachments
        ? _attachments.map((item) => item.id).toList(growable: false)
        : const <String>[];
    setState(() {
      _submitting = true;
      _error = null;
    });
    try {
      final repository = ref.read(agentTasksRepositoryProvider);
      final AgentTask task;
      if (provider.supportsResume && _continueContext) {
        final resumeTaskId = _resumeTaskId;
        if (resumeTaskId == null) {
          throw const AgentTasksApiException(
            statusCode: 400,
            code: 'oneshot.context_required',
            message: 'Select a task with a resumable runtime context',
            retryable: false,
          );
        }
        task = await repository.continueTask(
          resumeTaskId,
          ContinueAgentTaskInput(
            projectId: _projectId!,
            providerId: provider.providerId,
            workspacePath: _workspaceController.text.trim(),
            promptDelta: _promptController.text.trim(),
            attachmentRefs: attachmentRefs,
          ),
          idempotencyKey: key,
        );
      } else {
        task = await repository.createTask(
          CreateAgentTaskInput(
            projectId: _projectId!,
            providerId: provider.providerId,
            prompt: _promptController.text.trim(),
            workspacePath: _workspaceController.text.trim(),
            attachmentRefs: attachmentRefs,
            timeoutSeconds: int.parse(_timeoutController.text.trim()),
            telegramNotify: _telegramNotify,
          ),
          idempotencyKey: key,
        );
      }
      if (!mounted) return;
      context.pop(task.id);
    } on AgentTasksApiException catch (error) {
      if (!mounted) return;
      setState(() {
        _submitting = false;
        _error = error;
      });
    }
  }

  Future<void> _pickAttachments() async {
    final projectId = _projectId;
    if (projectId == null || _uploadingAttachment) return;
    final result = await FilePicker.platform.pickFiles(
      allowMultiple: true,
      withData: true,
    );
    if (result == null || !mounted) return;
    setState(() {
      _uploadingAttachment = true;
      _error = null;
    });
    try {
      final repository = ref.read(agentTasksRepositoryProvider);
      final staged = <StagedAgentAttachment>[];
      for (final file in result.files) {
        final bytes = file.bytes;
        if (bytes == null) {
          throw const AgentTasksApiException(
            statusCode: 400,
            code: 'oneshot.invalid_attachment',
            message: 'Selected file bytes are unavailable',
            retryable: false,
          );
        }
        staged.add(
          await repository.stageAttachment(
            projectId: projectId,
            fileName: file.name,
            bytes: bytes,
            mimeType: _mimeForExtension(file.extension),
          ),
        );
      }
      if (!mounted) return;
      setState(() {
        _attachments.addAll(staged);
        _uploadingAttachment = false;
        _idempotencyKey = null;
      });
    } on AgentTasksApiException catch (error) {
      if (!mounted) return;
      setState(() {
        _uploadingAttachment = false;
        _error = error;
      });
    }
  }

  Future<void> _removeAttachment(StagedAgentAttachment item) async {
    try {
      await ref
          .read(agentTasksRepositoryProvider)
          .deleteStagedAttachment(item.id, projectId: item.projectId);
      if (!mounted) return;
      setState(() {
        _attachments.removeWhere((value) => value.id == item.id);
        _idempotencyKey = null;
      });
    } on AgentTasksApiException catch (error) {
      if (!mounted) return;
      setState(() => _error = error);
    }
  }

  String? _mimeForExtension(String? extension) {
    switch (extension?.toLowerCase()) {
      case 'txt':
        return 'text/plain';
      case 'md':
        return 'text/markdown';
      case 'csv':
        return 'text/csv';
      case 'json':
        return 'application/json';
      case 'pdf':
        return 'application/pdf';
      case 'png':
        return 'image/png';
      case 'jpg':
      case 'jpeg':
        return 'image/jpeg';
      case 'gif':
        return 'image/gif';
      case 'webp':
        return 'image/webp';
      default:
        return null;
    }
  }

  @override
  Widget build(BuildContext context) {
    final strings = AgentTasksStrings.current;
    return Scaffold(
      appBar: AppBar(title: Text(strings('create'))),
      body:
          FutureBuilder<
            ({
              List<ProjectSummary> projects,
              List<AgentProviderCapability> providers,
              List<AgentTask> resumableTasks,
            })
          >(
            future: _options,
            builder: (context, snapshot) {
              if (snapshot.connectionState != ConnectionState.done) {
                return const Center(child: CircularProgressIndicator());
              }
              if (snapshot.hasError) {
                return _OptionsError(
                  error: snapshot.error!,
                  onRetry: () => setState(() => _options = _loadOptions()),
                );
              }
              final data = snapshot.data!;
              final selectedProvider = _selectedProvider(data.providers);
              return Form(
                key: _formKey,
                child: ListView(
                  padding: const EdgeInsets.fromLTRB(16, 16, 16, 32),
                  children: [
                    Card(
                      color: Theme.of(
                        context,
                      ).colorScheme.surfaceContainerHighest,
                      child: Padding(
                        padding: const EdgeInsets.all(12),
                        child: Row(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            const Icon(Icons.call_split),
                            const SizedBox(width: 10),
                            Expanded(child: Text(strings('sessionIsolation'))),
                          ],
                        ),
                      ),
                    ),
                    const SizedBox(height: 16),
                    LayoutBuilder(
                      builder: (context, constraints) {
                        final compact = constraints.maxWidth < 620;
                        final project = _projectField(data.projects);
                        final provider = _providerField(data.providers);
                        if (compact) {
                          return Column(
                            children: [
                              project,
                              const SizedBox(height: 12),
                              provider,
                            ],
                          );
                        }
                        return Row(
                          crossAxisAlignment: CrossAxisAlignment.start,
                          children: [
                            Expanded(child: project),
                            const SizedBox(width: 12),
                            Expanded(child: provider),
                          ],
                        );
                      },
                    ),
                    const SizedBox(height: 12),
                    TextFormField(
                      controller: _workspaceController,
                      decoration: InputDecoration(
                        labelText: strings('workspace'),
                        prefixIcon: const Icon(Icons.folder_outlined),
                      ),
                      validator: (value) =>
                          value == null || value.trim().isEmpty
                          ? strings('required')
                          : null,
                    ),
                    const SizedBox(height: 12),
                    TextFormField(
                      controller: _promptController,
                      minLines: 5,
                      maxLines: 12,
                      textInputAction: TextInputAction.newline,
                      decoration: InputDecoration(
                        labelText: strings('prompt'),
                        alignLabelWithHint: true,
                        prefixIcon: const Padding(
                          padding: EdgeInsets.only(bottom: 100),
                          child: Icon(Icons.edit_note),
                        ),
                      ),
                      validator: (value) =>
                          value == null || value.trim().isEmpty
                          ? strings('required')
                          : null,
                    ),
                    const SizedBox(height: 12),
                    if (selectedProvider?.supportsResume == true) ...[
                      SwitchListTile.adaptive(
                        value: _continueContext,
                        onChanged: (value) {
                          final compatible = _compatibleResumeTasks(
                            data.resumableTasks,
                            selectedProvider!,
                          );
                          setState(() {
                            _continueContext = value;
                            _resumeTaskId = value && compatible.isNotEmpty
                                ? compatible.first.id
                                : null;
                            _idempotencyKey = null;
                          });
                        },
                        secondary: const Icon(Icons.history),
                        title: Text(strings('contextMode')),
                        subtitle: Text(
                          _continueContext
                              ? strings('continueContext')
                              : strings('newContext'),
                        ),
                      ),
                      if (_continueContext)
                        Padding(
                          padding: const EdgeInsets.only(bottom: 12),
                          child: DropdownButtonFormField<String>(
                            initialValue: _resumeTaskId,
                            isExpanded: true,
                            decoration: InputDecoration(
                              labelText: strings('continueContext'),
                              prefixIcon: const Icon(
                                Icons.account_tree_outlined,
                              ),
                            ),
                            items: [
                              for (final task in _compatibleResumeTasks(
                                data.resumableTasks,
                                selectedProvider!,
                              ))
                                DropdownMenuItem(
                                  value: task.id,
                                  child: Text(
                                    '${task.prompt} · ${task.runtimeContextId}',
                                    overflow: TextOverflow.ellipsis,
                                  ),
                                ),
                            ],
                            onChanged: (value) => setState(() {
                              _resumeTaskId = value;
                              _idempotencyKey = null;
                            }),
                            validator: (value) =>
                                _continueContext && value == null
                                ? strings('required')
                                : null,
                          ),
                        ),
                    ],
                    if (selectedProvider?.attachments == true) ...[
                      const SizedBox(height: 8),
                      Align(
                        alignment: Alignment.centerLeft,
                        child: OutlinedButton.icon(
                          onPressed: _uploadingAttachment || _projectId == null
                              ? null
                              : _pickAttachments,
                          icon: _uploadingAttachment
                              ? const SizedBox.square(
                                  dimension: 16,
                                  child: CircularProgressIndicator(
                                    strokeWidth: 2,
                                  ),
                                )
                              : const Icon(Icons.attach_file),
                          label: Text(strings('pickAttachments')),
                        ),
                      ),
                      if (_attachments.isNotEmpty)
                        Wrap(
                          spacing: 8,
                          runSpacing: 8,
                          children: [
                            for (final item in _attachments)
                              InputChip(
                                label: Text(
                                  '${item.name} · ${item.sizeBytes} B',
                                ),
                                onDeleted: _submitting
                                    ? null
                                    : () => _removeAttachment(item),
                              ),
                          ],
                        ),
                    ],
                    if (!_continueContext) ...[
                      const SizedBox(height: 12),
                      TextFormField(
                        controller: _timeoutController,
                        keyboardType: TextInputType.number,
                        decoration: InputDecoration(
                          labelText: strings('timeout'),
                          prefixIcon: const Icon(Icons.timer_outlined),
                        ),
                        validator: (value) {
                          final seconds = int.tryParse(value?.trim() ?? '');
                          return seconds == null ||
                                  seconds < 30 ||
                                  seconds > 86400
                              ? strings('invalidTimeout')
                              : null;
                        },
                      ),
                      SwitchListTile.adaptive(
                        value: _telegramNotify,
                        onChanged: (value) => setState(() {
                          _telegramNotify = value;
                          _idempotencyKey = null;
                        }),
                        secondary: const Icon(Icons.send_outlined),
                        title: Text(strings('telegramNotify')),
                      ),
                    ],
                    if (_error != null) ...[
                      const SizedBox(height: 8),
                      _InlineError(error: _error!),
                    ],
                    const SizedBox(height: 20),
                    FilledButton.icon(
                      onPressed: _submitting
                          ? null
                          : () => _submit(data.providers),
                      icon: _submitting
                          ? const SizedBox.square(
                              dimension: 18,
                              child: CircularProgressIndicator(strokeWidth: 2),
                            )
                          : const Icon(Icons.play_arrow),
                      label: Text(
                        _submitting ? strings('creating') : strings('submit'),
                      ),
                    ),
                    const SizedBox(height: 8),
                    Text(
                      strings('idempotent'),
                      textAlign: TextAlign.center,
                      style: Theme.of(context).textTheme.bodySmall,
                    ),
                  ],
                ),
              );
            },
          ),
    );
  }

  Widget _projectField(List<ProjectSummary> projects) {
    final strings = AgentTasksStrings.current;
    return DropdownButtonFormField<String>(
      initialValue: _projectId,
      isExpanded: true,
      decoration: InputDecoration(
        labelText: strings('project'),
        prefixIcon: const Icon(Icons.folder_copy_outlined),
      ),
      items: [
        for (final project in projects)
          DropdownMenuItem(value: project.cwd, child: Text(project.cwd)),
      ],
      onChanged: (value) => setState(() {
        _projectId = value;
        _resumeTaskId = null;
        if (value != null) _workspaceController.text = value;
        _idempotencyKey = null;
      }),
      validator: (value) => value == null ? strings('required') : null,
    );
  }

  Widget _providerField(List<AgentProviderCapability> providers) {
    final strings = AgentTasksStrings.current;
    return DropdownButtonFormField<String>(
      initialValue: _providerId,
      isExpanded: true,
      decoration: InputDecoration(
        labelText: strings('provider'),
        prefixIcon: const Icon(Icons.smart_toy_outlined),
      ),
      items: [
        for (final provider in providers)
          DropdownMenuItem(
            value: provider.providerId,
            child: Text(provider.displayName),
          ),
      ],
      onChanged: (value) => setState(() {
        _providerId = value;
        _resumeTaskId = null;
        final provider = _selectedProvider(providers);
        if (provider?.supportsResume != true) _continueContext = false;
        if (provider?.attachments != true) _attachments.clear();
        _idempotencyKey = null;
      }),
      validator: (value) => value == null ? strings('required') : null,
    );
  }

  @override
  void dispose() {
    for (final controller in [
      _promptController,
      _workspaceController,
      _timeoutController,
    ]) {
      controller
        ..removeListener(_invalidateIdempotencyKey)
        ..dispose();
    }
    super.dispose();
  }
}

class _OptionsError extends StatelessWidget {
  const _OptionsError({required this.error, required this.onRetry});

  final Object error;
  final VoidCallback onRetry;

  @override
  Widget build(BuildContext context) {
    final strings = AgentTasksStrings.current;
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(32),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Icon(Icons.error_outline, size: 48),
            const SizedBox(height: 12),
            Text(error.toString(), textAlign: TextAlign.center),
            const SizedBox(height: 12),
            FilledButton.tonal(
              onPressed: onRetry,
              child: Text(strings('retry')),
            ),
          ],
        ),
      ),
    );
  }
}

class _InlineError extends StatelessWidget {
  const _InlineError({required this.error});

  final AgentTasksApiException error;

  @override
  Widget build(BuildContext context) {
    final strings = AgentTasksStrings.current;
    final message = error.isForbidden
        ? strings('permissionError')
        : error.isOffline
        ? strings('networkError')
        : error.message;
    return Card(
      color: Theme.of(context).colorScheme.errorContainer,
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Row(
          children: [
            Icon(
              Icons.error_outline,
              color: Theme.of(context).colorScheme.error,
            ),
            const SizedBox(width: 10),
            Expanded(child: Text(message)),
          ],
        ),
      ),
    );
  }
}
