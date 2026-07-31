import 'dart:async';

import 'package:file_picker/file_picker.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:intl/intl.dart';
import 'package:url_launcher/url_launcher.dart';

import 'package:opendray/features/agent_tasks/data/agent_tasks_api.dart';
import 'package:opendray/features/agent_tasks/domain/agent_task_models.dart';
import 'package:opendray/features/agent_tasks/presentation/agent_task_controllers.dart';
import 'package:opendray/features/agent_tasks/presentation/agent_tasks_strings.dart';
import 'package:opendray/features/agent_tasks/presentation/widgets/agent_task_status_badge.dart';

class AgentTaskDetailScreen extends ConsumerStatefulWidget {
  const AgentTaskDetailScreen({required this.taskId, super.key});

  final String taskId;

  @override
  ConsumerState<AgentTaskDetailScreen> createState() =>
      _AgentTaskDetailScreenState();
}

class _AgentTaskDetailScreenState
    extends ConsumerState<AgentTaskDetailScreen> {
  _OutputFilter _filter = _OutputFilter.stdout;

  @override
  Widget build(BuildContext context) {
    final provider = agentTaskDetailControllerProvider(widget.taskId);
    final state = ref.watch(provider);
    final controller = ref.read(provider.notifier);
    final strings = AgentTasksStrings.current;

    return Scaffold(
      appBar: AppBar(
        title: Text(strings('details')),
        actions: [
          IconButton(
            tooltip: strings('refresh'),
            onPressed: state.loading ? null : controller.refresh,
            icon: const Icon(Icons.refresh),
          ),
        ],
      ),
      body: state.loading && state.task == null
          ? const Center(child: CircularProgressIndicator())
          : state.task == null
              ? _FatalError(error: state.error, onRetry: controller.refresh)
              : RefreshIndicator(
                  onRefresh: controller.refresh,
                  child: ListView(
                    physics: const AlwaysScrollableScrollPhysics(),
                    padding: const EdgeInsets.fromLTRB(12, 12, 12, 32),
                    children: [
                      if (state.offline)
                        MaterialBanner(
                          content: Text(strings('offline')),
                          leading: const Icon(Icons.cloud_off),
                          actions: [
                            TextButton(
                              onPressed: controller.refresh,
                              child: Text(strings('retry')),
                            ),
                          ],
                        ),
                      _TaskOverview(task: state.task!),
                      const SizedBox(height: 16),
                      _SectionTitle(
                        icon: Icons.timeline,
                        title: strings('timeline'),
                      ),
                      const SizedBox(height: 8),
                      _StatusTimeline(task: state.task!, runs: state.runs),
                      const SizedBox(height: 12),
                      if (state.task!.status == AgentTaskStatus.waitingInput)
                        _WaitingInputCard(
                          loading: state.actionLoading,
                          onContinue: (value) => _runAction(
                            () => controller.continueTask(value),
                          ),
                        ),
                      _Controls(
                        task: state.task!,
                        loading: state.actionLoading,
                        onCancel: () async {
                          final ok = await _confirm(strings('confirmCancel'));
                          if (ok) await _runAction(controller.cancel);
                        },
                        onRetry: () async {
                          final ok = await _confirm(strings('confirmRetry'));
                          if (ok) await _runAction(controller.retry);
                        },
                        onContinue: () => _showContinue(controller),
                      ),
                      if (state.error != null) ...[
                        const SizedBox(height: 12),
                        _InlineError(error: state.error!),
                      ],
                      const SizedBox(height: 16),
                      _SectionTitle(
                        icon: Icons.history,
                        title: strings('runs'),
                      ),
                      const SizedBox(height: 8),
                      _RunSelector(
                        runs: state.runs,
                        selected: state.selectedRun,
                        onSelected: controller.selectRun,
                      ),
                      const SizedBox(height: 16),
                      _SectionTitle(
                        icon: Icons.terminal,
                        title: strings('events'),
                      ),
                      const SizedBox(height: 8),
                      _OutputToolbar(
                        filter: _filter,
                        onChanged: (value) => setState(() => _filter = value),
                      ),
                      const SizedBox(height: 8),
                      _OutputList(
                        events: state.events,
                        filter: _filter,
                      ),
                      if (state.events.isNotEmpty) ...[
                        const SizedBox(height: 16),
                        _SectionTitle(
                          icon: Icons.summarize_outlined,
                          title: strings('result'),
                        ),
                        const SizedBox(height: 8),
                        _FinalResultCard(events: state.events),
                      ],
                      const SizedBox(height: 16),
                      _SectionTitle(
                        icon: Icons.inventory_2_outlined,
                        title: strings('artifacts'),
                      ),
                      const SizedBox(height: 8),
                      _ArtifactList(
                        artifacts: state.artifacts,
                        onDownload: (artifact) =>
                            _downloadArtifact(controller, artifact),
                      ),
                    ],
                  ),
                ),
    );
  }

  Future<void> _runAction(Future<void> Function() action) async {
    try {
      await action();
    } on AgentTasksApiException catch (error) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text(error.message)),
      );
    }
  }

  Future<bool> _confirm(String message) async {
    final strings = AgentTasksStrings.current;
    return await showDialog<bool>(
          context: context,
          builder: (context) => AlertDialog(
            content: Text(message),
            actions: [
              TextButton(
                onPressed: () => Navigator.pop(context, false),
                child: Text(strings('cancelAction')),
              ),
              FilledButton(
                onPressed: () => Navigator.pop(context, true),
                child: Text(strings('confirm')),
              ),
            ],
          ),
        ) ??
        false;
  }

  Future<void> _showContinue(AgentTaskDetailController controller) async {
    final textController = TextEditingController();
    final strings = AgentTasksStrings.current;
    final value = await showDialog<String>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text(strings('continueAction')),
        content: TextField(
          controller: textController,
          minLines: 3,
          maxLines: 8,
          autofocus: true,
          decoration: InputDecoration(hintText: strings('continueHint')),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context),
            child: Text(strings('cancelAction')),
          ),
          FilledButton(
            onPressed: () {
              final value = textController.text.trim();
              if (value.isNotEmpty) Navigator.pop(context, value);
            },
            child: Text(strings('send')),
          ),
        ],
      ),
    );
    textController.dispose();
    if (value != null) await _runAction(() => controller.continueTask(value));
  }

  Future<void> _downloadArtifact(
    AgentTaskDetailController controller,
    AgentArtifact artifact,
  ) async {
    final strings = AgentTasksStrings.current;
    try {
      final download = await controller.downloadArtifact(artifact);
      final path = await FilePicker.platform.saveFile(
        dialogTitle: strings('download'),
        fileName: download.fileName,
        bytes: download.bytes,
      );
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(
          content: Text(
            '${strings('verified')}${path == null ? '' : ': $path'}',
          ),
          action: path == null
              ? null
              : SnackBarAction(
                  label: strings('open'),
                  onPressed: () => unawaited(launchUrl(Uri.file(path))),
                ),
        ),
      );
    } on Object catch (error) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('${strings('downloadFailed')}: $error')),
      );
    }
  }
}

class _StatusTimeline extends StatelessWidget {
  const _StatusTimeline({required this.task, required this.runs});

  final AgentTask task;
  final List<AgentRun> runs;

  @override
  Widget build(BuildContext context) {
    final strings = AgentTasksStrings.current;
    final entries = <({DateTime at, IconData icon, String title, String detail})>[
      (
        at: task.createdAt,
        icon: Icons.add_task,
        title: strings('created'),
        detail: task.source.kind,
      ),
      for (final run in runs.reversed)
        (
          at: run.startedAt ?? run.createdAt,
          icon: _runIcon(run.status),
          title: '${strings('runs')} · ${run.status.wire}',
          detail: run.id,
        ),
      (
        at: task.updatedAt,
        icon: Icons.flag_outlined,
        title: strings('status'),
        detail: task.status.wire,
      ),
    ]..sort((a, b) => a.at.compareTo(b.at));
    final date = DateFormat.yMd().add_Hms();
    return Card(
      child: Column(
        children: [
          for (var index = 0; index < entries.length; index++) ...[
            ListTile(
              dense: true,
              leading: Icon(entries[index].icon, size: 20),
              title: Text(entries[index].title),
              subtitle: Text(
                '${date.format(entries[index].at.toLocal())} · ${entries[index].detail}',
                overflow: TextOverflow.ellipsis,
              ),
            ),
            if (index < entries.length - 1) const Divider(height: 1),
          ],
        ],
      ),
    );
  }
}

class _FinalResultCard extends StatelessWidget {
  const _FinalResultCard({required this.events});

  final List<AgentEvent> events;

  @override
  Widget build(BuildContext context) {
    final candidates = events.where((event) {
      final type = event.type.toLowerCase();
      return type.contains('assistant') ||
          type.contains('result') ||
          type.contains('final');
    }).toList(growable: false);
    final event = candidates.isNotEmpty ? candidates.last : events.last;
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: SelectableText(event.displayText),
      ),
    );
  }
}

class _TaskOverview extends StatelessWidget {
  const _TaskOverview({required this.task});

  final AgentTask task;

  @override
  Widget build(BuildContext context) {
    final strings = AgentTasksStrings.current;
    final date = DateFormat.yMd().add_Hms();
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Expanded(
                  child: Text(
                    task.prompt,
                    style: Theme.of(context).textTheme.titleMedium?.copyWith(
                          fontWeight: FontWeight.w600,
                        ),
                  ),
                ),
                const SizedBox(width: 12),
                AgentTaskStatusBadge(status: task.status),
              ],
            ),
            const SizedBox(height: 14),
            _DetailRow(label: strings('project'), value: task.projectId),
            _DetailRow(label: strings('provider'), value: task.providerId),
            _DetailRow(label: strings('source'), value: task.source.kind),
            _DetailRow(
              label: strings('currentRun'),
              value: task.currentRunId ?? '—',
            ),
            _DetailRow(
              label: strings('context'),
              value: task.runtimeContextId ?? '—',
            ),
            _DetailRow(
              label: strings('created'),
              value: date.format(task.createdAt.toLocal()),
            ),
            _DetailRow(
              label: strings('updated'),
              value: date.format(task.updatedAt.toLocal()),
            ),
          ],
        ),
      ),
    );
  }
}

class _DetailRow extends StatelessWidget {
  const _DetailRow({required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) => Padding(
        padding: const EdgeInsets.only(top: 6),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            SizedBox(
              width: 110,
              child: Text(
                label,
                style: Theme.of(context).textTheme.bodySmall,
              ),
            ),
            Expanded(
              child: SelectableText(
                value,
                style: Theme.of(context).textTheme.bodySmall?.copyWith(
                      fontFamily: value.startsWith('ot') || value.startsWith('orn')
                          ? 'monospace'
                          : null,
                    ),
              ),
            ),
          ],
        ),
      );
}

class _Controls extends StatelessWidget {
  const _Controls({
    required this.task,
    required this.loading,
    required this.onCancel,
    required this.onRetry,
    required this.onContinue,
  });

  final AgentTask task;
  final bool loading;
  final VoidCallback onCancel;
  final VoidCallback onRetry;
  final VoidCallback onContinue;

  @override
  Widget build(BuildContext context) {
    final strings = AgentTasksStrings.current;
    final actions = <Widget>[];
    if (task.status.canContinue && task.runtimeContextId != null) {
      actions.add(
        FilledButton.tonalIcon(
          onPressed: loading ? null : onContinue,
          icon: const Icon(Icons.reply),
          label: Text(strings('continueAction')),
        ),
      );
    }
    if (task.status.canCancel) {
      actions.add(
        FilledButton.tonalIcon(
          onPressed: loading ? null : onCancel,
          icon: const Icon(Icons.stop_circle_outlined),
          label: Text(strings('cancelAction')),
        ),
      );
    }
    if (task.status.canRetry) {
      actions.add(
        OutlinedButton.icon(
          onPressed: loading ? null : onRetry,
          icon: const Icon(Icons.restart_alt),
          label: Text(strings('retryAction')),
        ),
      );
    }
    if (actions.isEmpty) return const SizedBox.shrink();
    return Padding(
      padding: const EdgeInsets.only(top: 12),
      child: Wrap(spacing: 8, runSpacing: 8, children: actions),
    );
  }
}

class _WaitingInputCard extends StatefulWidget {
  const _WaitingInputCard({
    required this.loading,
    required this.onContinue,
  });

  final bool loading;
  final ValueChanged<String> onContinue;

  @override
  State<_WaitingInputCard> createState() => _WaitingInputCardState();
}

class _WaitingInputCardState extends State<_WaitingInputCard> {
  final _controller = TextEditingController();

  @override
  Widget build(BuildContext context) {
    final strings = AgentTasksStrings.current;
    return Card(
      color: Theme.of(context).colorScheme.tertiaryContainer,
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                const Icon(Icons.input),
                const SizedBox(width: 8),
                Text(
                  strings('waitingInput'),
                  style: Theme.of(context).textTheme.titleSmall,
                ),
              ],
            ),
            const SizedBox(height: 10),
            TextField(
              controller: _controller,
              minLines: 2,
              maxLines: 6,
              decoration: InputDecoration(hintText: strings('continueHint')),
            ),
            const SizedBox(height: 8),
            Align(
              alignment: Alignment.centerRight,
              child: FilledButton.icon(
                onPressed: widget.loading
                    ? null
                    : () {
                        final value = _controller.text.trim();
                        if (value.isNotEmpty) widget.onContinue(value);
                      },
                icon: const Icon(Icons.send),
                label: Text(strings('send')),
              ),
            ),
          ],
        ),
      ),
    );
  }

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }
}

class _RunSelector extends StatelessWidget {
  const _RunSelector({
    required this.runs,
    required this.selected,
    required this.onSelected,
  });

  final List<AgentRun> runs;
  final AgentRun? selected;
  final ValueChanged<AgentRun> onSelected;

  @override
  Widget build(BuildContext context) {
    final strings = AgentTasksStrings.current;
    if (runs.isEmpty) {
      return Card(child: Padding(padding: const EdgeInsets.all(16), child: Text(strings('noRuns'))));
    }
    return SizedBox(
      height: 48,
      child: ListView.separated(
        scrollDirection: Axis.horizontal,
        itemCount: runs.length,
        separatorBuilder: (_, __) => const SizedBox(width: 8),
        itemBuilder: (context, index) {
          final run = runs[index];
          return ChoiceChip(
            selected: run.id == selected?.id,
            onSelected: (_) => onSelected(run),
            avatar: Icon(_runIcon(run.status), size: 16),
            label: Text('${index + 1} · ${run.status.wire}'),
          );
        },
      ),
    );
  }
}

class _OutputToolbar extends StatelessWidget {
  const _OutputToolbar({required this.filter, required this.onChanged});

  final _OutputFilter filter;
  final ValueChanged<_OutputFilter> onChanged;

  @override
  Widget build(BuildContext context) {
    final strings = AgentTasksStrings.current;
    return SegmentedButton<_OutputFilter>(
      segments: [
        ButtonSegment(value: _OutputFilter.stdout, label: Text(strings('stdout'))),
        ButtonSegment(value: _OutputFilter.stderr, label: Text(strings('stderr'))),
        ButtonSegment(value: _OutputFilter.raw, label: Text(strings('raw'))),
      ],
      selected: {filter},
      onSelectionChanged: (values) => onChanged(values.first),
    );
  }
}

class _OutputList extends StatelessWidget {
  const _OutputList({required this.events, required this.filter});

  final List<AgentEvent> events;
  final _OutputFilter filter;

  @override
  Widget build(BuildContext context) {
    final strings = AgentTasksStrings.current;
    final visible = events.where((event) {
      return switch (filter) {
        _OutputFilter.stdout => event.streamName == 'stdout',
        _OutputFilter.stderr => event.streamName == 'stderr',
        _OutputFilter.raw => true,
      };
    }).toList(growable: false);
    if (visible.isEmpty) {
      return Card(child: Padding(padding: const EdgeInsets.all(16), child: Text(strings('noOutput'))));
    }
    final itemHeight = filter == _OutputFilter.raw ? 110.0 : 72.0;
    final height =
        (visible.length * itemHeight).clamp(120.0, 520.0).toDouble();
    return Card(
      color: Colors.black,
      clipBehavior: Clip.antiAlias,
      child: SizedBox(
        height: height,
        child: ListView.builder(
          key: PageStorageKey('agent-output-${filter.name}'),
          itemExtent: filter == _OutputFilter.raw ? 110 : null,
          itemCount: visible.length,
          itemBuilder: (context, index) {
            final event = visible[index];
            return Container(
              padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
              decoration: const BoxDecoration(
                border: Border(bottom: BorderSide(color: Color(0x22333333))),
              ),
              child: SelectableText(
                filter == _OutputFilter.raw
                    ? '[${event.sequence}] ${event.type}\n${event.displayText}'
                    : event.displayText,
                style: TextStyle(
                  color: event.streamName == 'stderr'
                      ? Colors.orangeAccent
                      : Colors.greenAccent,
                  fontFamily: 'monospace',
                  fontSize: 12,
                  height: 1.35,
                ),
              ),
            );
          },
        ),
      ),
    );
  }
}

class _ArtifactList extends StatelessWidget {
  const _ArtifactList({required this.artifacts, required this.onDownload});

  final List<AgentArtifact> artifacts;
  final ValueChanged<AgentArtifact> onDownload;

  @override
  Widget build(BuildContext context) {
    final strings = AgentTasksStrings.current;
    if (artifacts.isEmpty) {
      return Card(child: Padding(padding: const EdgeInsets.all(16), child: Text('—')));
    }
    return Card(
      child: Column(
        children: [
          for (var index = 0; index < artifacts.length; index++) ...[
            ListTile(
              leading: const Icon(Icons.description_outlined),
              title: Text(artifacts[index].name),
              subtitle: Text(
                '${artifacts[index].contentType} · ${_formatBytes(artifacts[index].sizeBytes)}\n'
                'SHA-256 ${artifacts[index].sha256.substring(0, artifacts[index].sha256.length.clamp(0, 16).toInt())}…',
              ),
              isThreeLine: true,
              trailing: IconButton(
                tooltip: strings('download'),
                onPressed: () => onDownload(artifacts[index]),
                icon: const Icon(Icons.download),
              ),
            ),
            if (index < artifacts.length - 1) const Divider(height: 1),
          ],
        ],
      ),
    );
  }
}

class _SectionTitle extends StatelessWidget {
  const _SectionTitle({required this.icon, required this.title});

  final IconData icon;
  final String title;

  @override
  Widget build(BuildContext context) => Row(
        children: [
          Icon(icon, size: 20),
          const SizedBox(width: 8),
          Text(title, style: Theme.of(context).textTheme.titleMedium),
        ],
      );
}

class _InlineError extends StatelessWidget {
  const _InlineError({required this.error});

  final AgentTasksApiException error;

  @override
  Widget build(BuildContext context) => Card(
        color: Theme.of(context).colorScheme.errorContainer,
        child: Padding(
          padding: const EdgeInsets.all(12),
          child: Text('${error.code}: ${error.message}'),
        ),
      );
}

class _FatalError extends StatelessWidget {
  const _FatalError({required this.error, required this.onRetry});

  final AgentTasksApiException? error;
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
            const Icon(Icons.error_outline, size: 52),
            const SizedBox(height: 12),
            Text(error?.message ?? strings('actionFailed')),
            const SizedBox(height: 12),
            FilledButton.tonal(onPressed: onRetry, child: Text(strings('retry'))),
          ],
        ),
      ),
    );
  }
}

enum _OutputFilter { stdout, stderr, raw }

IconData _runIcon(AgentRunStatus status) => switch (status) {
      AgentRunStatus.running || AgentRunStatus.collectingOutput => Icons.play_circle,
      AgentRunStatus.completed => Icons.check_circle,
      AgentRunStatus.failed => Icons.error,
      AgentRunStatus.cancelled => Icons.cancel,
      AgentRunStatus.timedOut => Icons.timer_off,
      AgentRunStatus.waitingInput => Icons.input,
      _ => Icons.schedule,
    };

String _formatBytes(int value) {
  if (value < 1024) return '$value B';
  if (value < 1024 * 1024) return '${(value / 1024).toStringAsFixed(1)} KB';
  return '${(value / (1024 * 1024)).toStringAsFixed(1)} MB';
}
