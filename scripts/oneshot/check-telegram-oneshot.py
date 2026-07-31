#!/usr/bin/env python3
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2]
def text(p): return (ROOT/p).read_text(encoding='utf-8')
def require(cond,msg):
    if not cond: raise AssertionError(msg)

a=text('internal/oneshot/channeladapter/adapter.go')
app=text('internal/app/app.go')
store=text('internal/oneshot/store/auxiliary.go')
pty=text('internal/session/channeladapter/adapter.go')

for cmd in ['run','tasks','task','continue','cancel','retry']:
    require(f'Name: "{cmd}"' in a, f'missing /{cmd}')
require('req.Channel.Kind() != "telegram"' in a, 'Telegram-only execution guard missing')
require('DispatchNotHandled' in a and 'ParseCommand' in a, 'plain text fallback contract missing')
require('ResolveChannelBinding' in a and 'UpsertChannelBinding' in a, 'independent binding missing')
for field in ['ChannelID','ConversationID','ThreadID','SourceMessageID']:
    require(field in a, f'binding does not preserve {field}')
require('telegramKey' in a and 'SourceMessageID' in a, 'Telegram source idempotency key missing')
require('telegramReplyTo' in a and 'reply_to_outbound_msg_id' in a, 'reply routing is not tied to the exact outbound One-shot notification')
require('tg_user_id' in a, 'Telegram owner does not use stable numeric identity')
require('COALESCE(source_message_id' in store, 'binding lookup is not scoped to exact outbound message id')
require('oneshot_channel_bindings_owner_reply_key' in text('internal/store/migrations/0085_oneshot_control_plane.sql'), 'owner/reply unique binding index missing')
require('InboundPriority = 100' in a, 'One-shot priority missing')
require('InboundPriority = 1000' in pty, 'PTY fallback priority changed unexpectedly')
require('Register("oneshot"' in app and 'Register(\n\t\t"interactive"' in app, 'deterministic dual-domain dispatch wiring missing')
require('session/' not in '\n'.join(line for line in a.splitlines() if line.strip().startswith('"github.com/opendray')), 'One-shot adapter imports Session domain')
require('WHERE principal_kind=$1 AND principal_id=$2 AND channel_id=$3' in store, 'binding lookup does not isolate owner/channel')
print('OD-OS-20 isolated Telegram One-shot command source gate: PASS')
