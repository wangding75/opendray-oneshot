import 'dart:async';

import 'package:dio/dio.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:opendray/core/api/project_docs_api.dart';
import 'package:opendray/features/agent_tasks/data/agent_tasks_api.dart';
import 'package:opendray/features/agent_tasks/data/agent_tasks_repository.dart';
import 'package:opendray/features/agent_tasks/domain/agent_task_models.dart';
import 'package:opendray/features/agent_tasks/presentation/create_agent_task_screen.dart';
import 'package:opendray/features/agent_tasks/presentation/widgets/agent_task_status_badge.dart';

class _FakeRepository extends AgentTasksRepository {
  _FakeRepository({required this.capability})
    : super(api: AgentTasksApi(Dio()), streams: null);

  final AgentProviderCapability capability;
  int createCalls = 0;
  final Completer<AgentTask> createCompleter = Completer<AgentTask>();

  @override
  Future<List<ProjectSummary>> listProjects() async => [
    ProjectSummary(
      cwd: '/repo',
      status: 'active',
      updatedBy: 'operator',
      lastActivityAt: DateTime.utc(2026, 7, 28),
      idleDays: 0,
      suggestArchive: false,
    ),
  ];

  @override
  Future<List<AgentProviderCapability>> listProviderCapabilities() async => [
    capability,
  ];

  @override
  Future<AgentPage<AgentTask>> listTasks({
    String? cursor,
    String? projectId,
    AgentTaskStatus? status,
  }) async => const AgentPage(items: []);

  @override
  Future<AgentTask> createTask(
    CreateAgentTaskInput input, {
    String? idempotencyKey,
  }) {
    createCalls++;
    return createCompleter.future;
  }
}

AgentProviderCapability _capability({
  bool resume = false,
  bool attachments = false,
}) => AgentProviderCapability(
  providerId: 'codex',
  displayName: 'Codex CLI',
  enabled: true,
  supportsNonInteractive: true,
  supportsResume: resume,
  structuredOutput: true,
  attachments: attachments,
  cancellation: true,
);

void main() {
  testWidgets('All frozen Task statuses render a semantic badge', (
    tester,
  ) async {
    for (final status in AgentTaskStatus.values.where(
      (status) => status != AgentTaskStatus.unknown,
    )) {
      await tester.pumpWidget(
        MaterialApp(
          home: Scaffold(body: AgentTaskStatusBadge(status: status)),
        ),
      );
      expect(find.byType(AgentTaskStatusBadge), findsOneWidget);
      expect(tester.takeException(), isNull);
    }
  });

  testWidgets('Create form stacks on a 320px screen without overflow', (
    tester,
  ) async {
    final repository = _FakeRepository(capability: _capability());
    await tester.binding.setSurfaceSize(const Size(320, 700));
    addTearDown(() => tester.binding.setSurfaceSize(null));

    await tester.pumpWidget(
      ProviderScope(
        overrides: [agentTasksRepositoryProvider.overrideWithValue(repository)],
        child: const MaterialApp(home: CreateAgentTaskScreen()),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.byType(CreateAgentTaskScreen), findsOneWidget);
    expect(find.byType(TextFormField), findsNWidgets(3));
    expect(find.text('Continue context'), findsNothing);
    expect(find.text('Attachments'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('Provider capability reveals resume and attachment controls', (
    tester,
  ) async {
    final repository = _FakeRepository(
      capability: _capability(resume: true, attachments: true),
    );
    await tester.pumpWidget(
      ProviderScope(
        overrides: [agentTasksRepositoryProvider.overrideWithValue(repository)],
        child: const MaterialApp(home: CreateAgentTaskScreen()),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Context'), findsWidgets);
    expect(find.widgetWithText(OutlinedButton, 'Choose files'), findsOneWidget);
  });

  testWidgets('Duplicate submit taps dispatch only one create request', (
    tester,
  ) async {
    final repository = _FakeRepository(capability: _capability());
    await tester.pumpWidget(
      ProviderScope(
        overrides: [agentTasksRepositoryProvider.overrideWithValue(repository)],
        child: const MaterialApp(home: CreateAgentTaskScreen()),
      ),
    );
    await tester.pumpAndSettle();

    final fields = find.byType(TextFormField);
    await tester.enterText(fields.at(1), 'Implement a safe feature');
    final submit = find.widgetWithText(FilledButton, 'Create task');
    final scrollable = find
        .descendant(
          of: find.byType(Form),
          matching: find.byType(Scrollable),
        )
        .first;
    await tester.dragUntilVisible(
      submit,
      scrollable,
      const Offset(0, -300),
    );

    expect(submit, findsOneWidget);

    await tester.tap(submit);
    await tester.tap(submit);
    await tester.pump();

    expect(repository.createCalls, 1);
  });
}
