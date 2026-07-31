import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:intl/intl.dart';

import 'package:opendray/features/agent_tasks/data/agent_tasks_api.dart';
import 'package:opendray/features/agent_tasks/domain/agent_task_models.dart';
import 'package:opendray/features/agent_tasks/presentation/agent_task_controllers.dart';
import 'package:opendray/features/agent_tasks/presentation/agent_tasks_strings.dart';
import 'package:opendray/features/agent_tasks/presentation/widgets/agent_task_status_badge.dart';

class AgentTasksScreen extends ConsumerWidget {
  const AgentTasksScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final state = ref.watch(agentTaskListControllerProvider);
    final controller = ref.read(agentTaskListControllerProvider.notifier);
    final strings = AgentTasksStrings.current;

    return Scaffold(
      appBar: AppBar(
        title: Text(strings('title')),
        actions: [
          ref.watch(agentTaskProjectOptionsProvider).when(
                data: (projects) => PopupMenuButton<String>(
                  tooltip: strings('project'),
                  initialValue: state.projectId ?? '',
                  icon: const Icon(Icons.folder_copy_outlined),
                  onSelected: (value) => controller.setProject(
                    value.isEmpty ? null : value,
                  ),
                  itemBuilder: (context) => [
                    PopupMenuItem(
                      value: '',
                      child: Text(strings('allProjects')),
                    ),
                    for (final project in projects)
                      PopupMenuItem(
                        value: project.cwd,
                        child: ConstrainedBox(
                          constraints: const BoxConstraints(maxWidth: 260),
                          child: Text(
                            project.cwd,
                            overflow: TextOverflow.ellipsis,
                          ),
                        ),
                      ),
                  ],
                ),
                error: (_, __) => const SizedBox.shrink(),
                loading: () => const SizedBox.square(dimension: 48),
              ),
          IconButton(
            tooltip: strings('refresh'),
            onPressed: state.loading ? null : controller.refresh,
            icon: const Icon(Icons.refresh),
          ),
        ],
        bottom: PreferredSize(
          preferredSize: const Size.fromHeight(52),
          child: _StatusFilter(
            selected: state.status,
            onChanged: controller.setStatus,
          ),
        ),
      ),
      body: Column(
        children: [
          if (state.offline)
            MaterialBanner(
              content: Text(strings('offline')),
              leading: const Icon(Icons.cloud_off),
              actions: [
                TextButton(onPressed: controller.refresh, child: Text(strings('retry'))),
              ],
            ),
          if (state.error != null && state.items.isNotEmpty)
            MaterialBanner(
              content: Text(_errorMessage(state.error!, strings)),
              leading: const Icon(Icons.error_outline),
              actions: [
                TextButton(onPressed: controller.refresh, child: Text(strings('retry'))),
              ],
            ),
          Expanded(
            child: RefreshIndicator(
              onRefresh: controller.refresh,
              child: _body(context, state, controller, strings),
            ),
          ),
        ],
      ),
      floatingActionButton: FloatingActionButton.extended(
        heroTag: 'agent_tasks_fab',
        onPressed: () async {
          final created = await context.push<String>('/agent-tasks/new');
          if (created != null && context.mounted) {
            await controller.refresh();
            if (context.mounted) await context.push('/agent-tasks/$created');
          }
        },
        icon: const Icon(Icons.add_task),
        label: Text(strings('create')),
      ),
    );
  }

  Widget _body(
    BuildContext context,
    AgentTaskListState state,
    AgentTaskListController controller,
    AgentTasksStrings strings,
  ) {
    if (state.loading && state.items.isEmpty) {
      return const Center(child: CircularProgressIndicator());
    }
    if (state.error != null && state.items.isEmpty) {
      return ListView(
        physics: const AlwaysScrollableScrollPhysics(),
        children: [
          const SizedBox(height: 120),
          Icon(Icons.error_outline, size: 52, color: Theme.of(context).colorScheme.error),
          const SizedBox(height: 16),
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 32),
            child: Text(
              _errorMessage(state.error!, strings),
              textAlign: TextAlign.center,
            ),
          ),
          const SizedBox(height: 16),
          Center(
            child: FilledButton.tonal(
              onPressed: controller.refresh,
              child: Text(strings('retry')),
            ),
          ),
        ],
      );
    }
    if (state.items.isEmpty) {
      return ListView(
        physics: const AlwaysScrollableScrollPhysics(),
        padding: const EdgeInsets.symmetric(horizontal: 32),
        children: [
          const SizedBox(height: 110),
          Icon(Icons.task_alt, size: 64, color: Theme.of(context).colorScheme.primary),
          const SizedBox(height: 20),
          Text(
            strings('emptyTitle'),
            textAlign: TextAlign.center,
            style: Theme.of(context).textTheme.titleLarge,
          ),
          const SizedBox(height: 10),
          Text(strings('emptyBody'), textAlign: TextAlign.center),
          const SizedBox(height: 12),
          Text(
            strings('sessionIsolation'),
            textAlign: TextAlign.center,
            style: Theme.of(context).textTheme.bodySmall,
          ),
        ],
      );
    }
    return ListView.separated(
      physics: const AlwaysScrollableScrollPhysics(),
      padding: const EdgeInsets.fromLTRB(12, 12, 12, 96),
      itemCount: state.items.length + (state.nextCursor == null ? 0 : 1),
      separatorBuilder: (_, __) => const SizedBox(height: 8),
      itemBuilder: (context, index) {
        if (index == state.items.length) {
          return Center(
            child: OutlinedButton.icon(
              onPressed: state.loadingMore ? null : controller.loadMore,
              icon: state.loadingMore
                  ? const SizedBox.square(
                      dimension: 16,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    )
                  : const Icon(Icons.expand_more),
              label: Text(strings('loadMore')),
            ),
          );
        }
        final task = state.items[index];
        return _TaskCard(task: task);
      },
    );
  }
}

class _StatusFilter extends StatelessWidget {
  const _StatusFilter({required this.selected, required this.onChanged});

  final AgentTaskStatus? selected;
  final ValueChanged<AgentTaskStatus?> onChanged;

  @override
  Widget build(BuildContext context) {
    final strings = AgentTasksStrings.current;
    final values = <AgentTaskStatus?>[
      null,
      AgentTaskStatus.pending,
      AgentTaskStatus.queued,
      AgentTaskStatus.running,
      AgentTaskStatus.waitingInput,
      AgentTaskStatus.completed,
      AgentTaskStatus.failed,
      AgentTaskStatus.cancelled,
      AgentTaskStatus.timedOut,
    ];
    return Semantics(
      label: strings('filterStatus'),
      child: SizedBox(
        height: 52,
        child: ListView.separated(
          scrollDirection: Axis.horizontal,
          padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
          itemCount: values.length,
          separatorBuilder: (_, __) => const SizedBox(width: 8),
          itemBuilder: (context, index) {
            final value = values[index];
            return ChoiceChip(
              label: Text(value == null ? strings('all') : _statusText(value, strings)),
              selected: value == selected,
              onSelected: (_) => onChanged(value),
            );
          },
        ),
      ),
    );
  }
}

class _TaskCard extends StatelessWidget {
  const _TaskCard({required this.task});

  final AgentTask task;

  @override
  Widget build(BuildContext context) {
    final strings = AgentTasksStrings.current;
    return Card(
      clipBehavior: Clip.antiAlias,
      child: InkWell(
        onTap: () => context.push('/agent-tasks/${task.id}'),
        child: Padding(
          padding: const EdgeInsets.all(14),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  Expanded(
                    child: Text(
                      task.prompt,
                      maxLines: 2,
                      overflow: TextOverflow.ellipsis,
                      style: Theme.of(context).textTheme.titleSmall?.copyWith(
                            fontWeight: FontWeight.w600,
                          ),
                    ),
                  ),
                  const SizedBox(width: 10),
                  AgentTaskStatusBadge(status: task.status),
                ],
              ),
              const SizedBox(height: 10),
              Wrap(
                spacing: 12,
                runSpacing: 4,
                children: [
                  _Meta(icon: Icons.folder_outlined, text: task.projectId),
                  _Meta(icon: Icons.smart_toy_outlined, text: task.providerId),
                  _Meta(
                    icon: _sourceIcon(task.source.kind),
                    text: _sourceText(task.source.kind, strings),
                  ),
                  _Meta(
                    icon: Icons.schedule,
                    text: DateFormat.yMd().add_Hm().format(task.updatedAt.toLocal()),
                  ),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _Meta extends StatelessWidget {
  const _Meta({required this.icon, required this.text});

  final IconData icon;
  final String text;

  @override
  Widget build(BuildContext context) => Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(icon, size: 14),
          const SizedBox(width: 4),
          ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 170),
            child: Text(
              text,
              overflow: TextOverflow.ellipsis,
              style: Theme.of(context).textTheme.bodySmall,
            ),
          ),
        ],
      );
}

String _errorMessage(Object error, AgentTasksStrings strings) {
  if (error is AgentTasksApiException) {
    if (error.isForbidden) return strings('permissionError');
    if (error.isOffline) return strings('networkError');
    return error.message;
  }
  return error.toString();
}

String _statusText(AgentTaskStatus status, AgentTasksStrings strings) => switch (status) {
      AgentTaskStatus.pending => strings('pending'),
      AgentTaskStatus.queued => strings('queued'),
      AgentTaskStatus.running => strings('running'),
      AgentTaskStatus.waitingInput => strings('waiting'),
      AgentTaskStatus.completed => strings('completed'),
      AgentTaskStatus.failed => strings('failed'),
      AgentTaskStatus.cancelled => strings('cancelled'),
      AgentTaskStatus.timedOut => strings('timedOut'),
      AgentTaskStatus.unknown => strings('unknown'),
    };

IconData _sourceIcon(String source) => switch (source) {
      'telegram' => Icons.send_outlined,
      'mobile' => Icons.phone_android,
      'api' => Icons.api,
      _ => Icons.hub_outlined,
    };

String _sourceText(String source, AgentTasksStrings strings) => switch (source) {
      'telegram' => strings('telegramSource'),
      'mobile' => strings('mobileSource'),
      'api' => strings('apiSource'),
      _ => strings('unknownSource'),
    };
