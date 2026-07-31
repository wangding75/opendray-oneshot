#!/usr/bin/env python3
from pathlib import Path
import sys

ROOT=Path(__file__).resolve().parents[2]
def text(p): return (ROOT/p).read_text(encoding='utf-8')
def require(cond,msg):
    if not cond: raise AssertionError(msg)

h=text('internal/oneshot/api/handler.go')
control=text('internal/oneshot/application/control_service.go')
store=text('internal/oneshot/store/task_delivery.go')
app=text('internal/app/app.go')
contract=text('docs/development/oneshot/contracts/http-api.md')

routes=[
('POST','/tasks'),('GET','/tasks'),('GET','/tasks/stream'),('GET','/tasks/{task_id}'),
('POST','/tasks/{task_id}/continue'),('POST','/tasks/{task_id}/cancel'),('POST','/tasks/{task_id}/retry'),
('GET','/tasks/{task_id}/runs'),('GET','/runs/{run_id}'),('GET','/runs/{run_id}/events'),
('GET','/runs/{run_id}/stream'),('GET','/runs/{run_id}/artifacts'),('GET','/artifacts/{artifact_id}'),
('POST','/attachments'),('GET','/attachments/{attachment_id}'),('DELETE','/attachments/{attachment_id}')]
for method,path in routes:
    go_path=path.replace('{task_id}','{task_id}').replace('{run_id}','{run_id}').replace('{artifact_id}','{artifact_id}')
    require(f'r.{method.title()}("{go_path}"' in h, f'missing route {method} {path}')
require(h.index('r.Get("/tasks/stream"') < h.index('r.Get("/tasks/{task_id}"'), 'static task stream must mount before task id')
for scope in ['oneshot:task:create','oneshot:task:read','oneshot:task:continue','oneshot:task:cancel','oneshot:task:retry','oneshot:run:read','oneshot:artifact:read']:
    require(scope in h, f'missing scope {scope}')
for action in ['oneshot.task.create','oneshot.task.list','oneshot.task.read','oneshot.task.continue','oneshot.task.cancel','oneshot.task.retry','oneshot.run.list','oneshot.run.read','oneshot.run.events.read','oneshot.run.artifacts.read','oneshot.artifact.read','oneshot.attachment.stage','oneshot.attachment.read','oneshot.attachment.delete','oneshot.task.stream','oneshot.run.stream']:
    require(action in h, f'missing frozen audit action {action}')
require('auditKeyHash' in h and 'sha256:' in h, 'raw idempotency key is not hashed before audit')
require('decodeEventCursor' in h and 'encodeEventCursor' in h, 'run event pagination cursor is not opaque')
for op in ['createTask','continueTask','retryTask']:
    segment=h[h.index(f'func (h *Handler) {op}'):]
    segment=segment[:segment.index('\nfunc ',10)]
    require('requireIdempotencyKey' in segment, f'{op} does not require idempotency')
require('requestIDMiddleware' in h and 'X-Request-ID' in h, 'stable request id middleware missing')
require('auditSuccess' in h and 'auditFailure' in h and 'oneshot.audit' in text('internal/oneshot/api/audit.go'), 'audit path missing')
require('auditRejectedWrite' in h and 'projectAllowed' in h, 'write validation or denied project audit path missing')
require('"provider_id": result.Task.ProviderID' in h and '"source": result.Task.Source.Kind' in h, 'write audit metadata is incomplete')
require('CancelActiveRun' in control and 'TerminateExistingTree' in control, 'cancel does not control real process tree')
require('CreateRetryDelivery' in control and 'FindRetryReplay' in control, 'retry does not use atomic idempotent persistence')
require('IsoLevel: pgx.Serializable' in store and 'CreateRetryDelivery' in store, 'retry idempotency transaction missing')
require('oneshootHandlers.Mount(r)' in app, 'Gateway route wiring missing')
require('/api/v1/oneshot' in contract, 'frozen contract missing')
require('attachmentservice.StageRequest' in text('internal/oneshot/api/attachments.go'), 'attachment staging service is not wired')
require('http.MaxBytesReader' in text('internal/oneshot/api/attachments.go'), 'attachment upload has no request size guard')
require('attachment extension approved by `OD-OS-24`' in contract, 'attachment route extension is not governed')
print('OD-OS-18 secure REST control-plane source gate: PASS')
