#!/usr/bin/env python3
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2]
def text(p): return (ROOT/p).read_text(encoding='utf-8')
def require(cond,msg):
    if not cond: raise AssertionError(msg)

h=text('internal/oneshot/api/handler.go')
out=text('internal/oneshot/store/output.go')
life=text('internal/oneshot/store/lifecycle.go')
aux=text('internal/oneshot/store/auxiliary.go')
notify=text('internal/oneshot/notification/outbox.go')
run=text('internal/oneshot/executor/run_service.go')
mig=text('internal/store/migrations/0085_oneshot_control_plane.sql')
events=text('docs/development/oneshot/contracts/events.md')

require('scopeTaskSubscribe' in h and 'scopeRunSubscribe' in h, 'WS subscription scopes missing')
require('ListRunReplayEvents' in h and 'ListTaskReplayEvents' in h, 'WS does not replay durable lifecycle/output rows')
require('cursor' in h and 'SetWriteDeadline' in h and 'defaultStreamBuffer' in h, 'cursor/slow-consumer policy missing')
require('event.Topic' in h and 'oneshot.task.output' not in h, 'WS must use frozen durable event topics')
require('session.' not in '\n'.join(line for line in h.splitlines() if 'Topic:' in line), 'session namespace leaked into One-shot stream')
require('PersistOutputBatch' in out and 'tx.Commit' in out, 'output events are not persisted atomically')
require('oneshot_lifecycle_events' in mig and 'insertTaskLifecycle' in life and 'insertRunLifecycle' in life, 'durable lifecycle event persistence missing')
require('UNIQUE (aggregate_kind,aggregate_id,topic)' not in mig, 'Task lifecycle topics must be repeatable across retry/continue cycles')
require('oneshot_lifecycle_events_run_topic_key' in mig, 'Run lifecycle deduplication index missing')
require('insertTerminalNotification' in life and 'FinalizeRunWithTask' in text('internal/oneshot/store/run.go'), 'terminal notification is not transactionally coupled to finalization')
require('FOR UPDATE OF n SKIP LOCKED' in aux, 'outbox claim is not concurrency safe')
for symbol in ['ClaimNotifications','MarkNotificationDelivered','RetryNotification']:
    require(symbol in aux, f'missing outbox operation {symbol}')
require('EnqueueRunTerminal' in notify and 'IdempotencyKey: "terminal:" + run.ID' in notify, 'terminal notification idempotency missing')
require('WithRunNotificationSink' in run and 'EnqueueRunTerminal' in run, 'RunService notification integration missing')
require('retryDelay' in notify, 'notification retry missing')
require('delivery_idempotency_key' in notify and 'UpsertChannelBinding' in notify and 'MetaOutboundMessageID' in notify, 'notification delivery cannot recover exact reply binding idempotently')
require('internal/oneshot/executor' not in notify, 'notification worker must not invoke or rerun provider execution')
require('oneshot_notification_outbox_lease_idx' in mig, 'outbox lease index missing')
require('Namespace: `oneshot.*`' in events, 'frozen namespace missing')
print('OD-OS-19 replayable WebSocket and notification outbox source gate: PASS')
