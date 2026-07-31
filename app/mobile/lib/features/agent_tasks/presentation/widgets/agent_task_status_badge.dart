import 'package:flutter/material.dart';

import 'package:opendray/features/agent_tasks/domain/agent_task_models.dart';
import 'package:opendray/features/agent_tasks/presentation/agent_tasks_strings.dart';

class AgentTaskStatusBadge extends StatelessWidget {
  const AgentTaskStatusBadge({required this.status, super.key});

  final AgentTaskStatus status;

  @override
  Widget build(BuildContext context) {
    final strings = AgentTasksStrings.current;
    final (background, foreground, label) = switch (status) {
      AgentTaskStatus.pending => (
          Colors.blueGrey.shade800,
          Colors.blueGrey.shade100,
          strings('pending'),
        ),
      AgentTaskStatus.queued => (
          Colors.indigo.shade900,
          Colors.indigo.shade100,
          strings('queued'),
        ),
      AgentTaskStatus.running => (
          Colors.green.shade900,
          Colors.greenAccent.shade100,
          strings('running'),
        ),
      AgentTaskStatus.waitingInput => (
          Colors.amber.shade900,
          Colors.amberAccent.shade100,
          strings('waiting'),
        ),
      AgentTaskStatus.completed => (
          Colors.teal.shade900,
          Colors.tealAccent.shade100,
          strings('completed'),
        ),
      AgentTaskStatus.failed => (
          Colors.red.shade900,
          Colors.redAccent.shade100,
          strings('failed'),
        ),
      AgentTaskStatus.cancelled => (
          Colors.grey.shade800,
          Colors.grey.shade300,
          strings('cancelled'),
        ),
      AgentTaskStatus.timedOut => (
          Colors.deepOrange.shade900,
          Colors.deepOrangeAccent.shade100,
          strings('timedOut'),
        ),
      AgentTaskStatus.unknown => (
          Colors.grey.shade800,
          Colors.grey.shade300,
          strings('unknown'),
        ),
    };
    return Semantics(
      label: '${strings('status')}: $label',
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
        decoration: BoxDecoration(
          color: background.withValues(alpha: 0.5),
          border: Border.all(color: foreground.withValues(alpha: 0.45)),
          borderRadius: BorderRadius.circular(999),
        ),
        child: Text(
          label,
          style: Theme.of(context).textTheme.labelSmall?.copyWith(
                color: foreground,
                fontWeight: FontWeight.w600,
              ),
        ),
      ),
    );
  }
}
