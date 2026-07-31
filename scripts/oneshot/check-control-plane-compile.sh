#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/internal/oneshot" "$TMP/internal" "$TMP/stubs/pgx/pgconn" "$TMP/stubs/pgx/pgxpool" "$TMP/stubs/chi" "$TMP/stubs/websocket"
cp -R internal/oneshot/domain internal/oneshot/attachment internal/oneshot/adapter internal/oneshot/application internal/oneshot/queue internal/oneshot/saga internal/oneshot/store internal/oneshot/api internal/oneshot/channeladapter internal/oneshot/notification internal/oneshot/appwire "$TMP/internal/oneshot/"
rm -f "$TMP/internal/oneshot/store/postgres_integration_test.go"
cat > "$TMP/go.mod" <<'MOD'
module github.com/opendray/opendray-v2

go 1.23.0

require (
 github.com/jackc/pgx/v5 v5.0.0
 github.com/go-chi/chi/v5 v5.0.0
 github.com/gorilla/websocket v1.0.0
)
replace github.com/jackc/pgx/v5 => ./stubs/pgx
replace github.com/go-chi/chi/v5 => ./stubs/chi
replace github.com/gorilla/websocket => ./stubs/websocket
MOD
cat > "$TMP/stubs/pgx/go.mod" <<'MOD'
module github.com/jackc/pgx/v5
go 1.23.0
MOD
cat > "$TMP/stubs/pgx/pgconn/pgconn.go" <<'GO'
package pgconn
type PgError struct { Code string; ConstraintName string }
func (e *PgError) Error() string { return e.Code }
type CommandTag struct{ N int64 }
func (c CommandTag) RowsAffected() int64 { return c.N }
GO
cat > "$TMP/stubs/pgx/pgx.go" <<'GO'
package pgx
import("context";"errors";"github.com/jackc/pgx/v5/pgconn")
var ErrNoRows=errors.New("no rows")
type Row interface{ Scan(...any) error }
type Rows interface{ Next() bool; Scan(...any) error; Close(); Err() error }
type TxIsoLevel string
const(Serializable TxIsoLevel="serializable"; ReadCommitted TxIsoLevel="read committed")
type TxOptions struct{ IsoLevel TxIsoLevel }
type Tx interface{ Exec(context.Context,string,...any)(pgconn.CommandTag,error); Query(context.Context,string,...any)(Rows,error); QueryRow(context.Context,string,...any) Row; Commit(context.Context) error; Rollback(context.Context) error }
GO
cat > "$TMP/stubs/pgx/pgxpool/pgxpool.go" <<'GO'
package pgxpool
import("context";"github.com/jackc/pgx/v5";"github.com/jackc/pgx/v5/pgconn")
type Pool struct{}
func(*Pool)Begin(context.Context)(pgx.Tx,error){return nil,nil}
func(*Pool)BeginTx(context.Context,pgx.TxOptions)(pgx.Tx,error){return nil,nil}
func(*Pool)Query(context.Context,string,...any)(pgx.Rows,error){return nil,nil}
func(*Pool)QueryRow(context.Context,string,...any)pgx.Row{return nil}
func(*Pool)Exec(context.Context,string,...any)(pgconn.CommandTag,error){return pgconn.CommandTag{},nil}
GO
cat > "$TMP/stubs/chi/go.mod" <<'MOD'
module github.com/go-chi/chi/v5
go 1.23.0
MOD
cat > "$TMP/stubs/chi/chi.go" <<'GO'
package chi
import("context";"net/http";"strings")
type Router interface{ http.Handler; Route(string,func(Router)); Use(...func(http.Handler)http.Handler); Post(string,http.HandlerFunc); Get(string,http.HandlerFunc); Delete(string,http.HandlerFunc) }
type route struct{method,path string; handler http.Handler}
type mux struct{prefix string; routes *[]route}
type paramsKey struct{}
func NewRouter() Router { routes:=[]route{}; return &mux{routes:&routes} }
func match(pattern,path string)(map[string]string,bool){p:=strings.Split(strings.Trim(pattern,"/"),"/");a:=strings.Split(strings.Trim(path,"/"),"/");if len(p)!=len(a){return nil,false};out:=map[string]string{};for i:=range p{if strings.HasPrefix(p[i],"{")&&strings.HasSuffix(p[i],"}"){out[strings.TrimSuffix(strings.TrimPrefix(p[i],"{"),"}")]=a[i];continue};if p[i]!=a[i]{return nil,false}};return out,true}
func (m *mux) ServeHTTP(w http.ResponseWriter,r *http.Request){for _,item:=range *m.routes{if item.method!=r.Method{continue};params,ok:=match(item.path,r.URL.Path);if !ok{continue};item.handler.ServeHTTP(w,r.WithContext(context.WithValue(r.Context(),paramsKey{},params)));return};http.NotFound(w,r)}
func (m *mux) Route(path string, fn func(Router)){ fn(&mux{prefix:m.prefix+path,routes:m.routes}) }
func (*mux) Use(...func(http.Handler)http.Handler){}
func (m *mux) Post(path string,h http.HandlerFunc){*m.routes=append(*m.routes,route{"POST",m.prefix+path,h})}
func (m *mux) Get(path string,h http.HandlerFunc){*m.routes=append(*m.routes,route{"GET",m.prefix+path,h})}
func (m *mux) Delete(path string,h http.HandlerFunc){*m.routes=append(*m.routes,route{"DELETE",m.prefix+path,h})}
func Walk(r Router, fn func(string,string,http.Handler,...func(http.Handler)http.Handler)error)error{m:=r.(*mux);for _,item:=range *m.routes{if err:=fn(item.method,item.path,item.handler);err!=nil{return err}};return nil}
func URLParam(r *http.Request,key string)string{params,_:=r.Context().Value(paramsKey{}).(map[string]string);return params[key]}
GO
cat > "$TMP/stubs/websocket/go.mod" <<'MOD'
module github.com/gorilla/websocket
go 1.23.0
MOD
cat > "$TMP/stubs/websocket/websocket.go" <<'GO'
package websocket
import("net/http";"time")
type Upgrader struct{ CheckOrigin func(*http.Request)bool }
func(Upgrader)Upgrade(http.ResponseWriter,*http.Request,http.Header)(*Conn,error){return &Conn{},nil}
type Conn struct{}
func(*Conn)Close()error{return nil}
func(*Conn)WriteJSON(any)error{return nil}
func(*Conn)SetWriteDeadline(time.Time)error{return nil}
GO
mkdir -p "$TMP/internal/integration" "$TMP/internal/eventbus" "$TMP/internal/channel" "$TMP/internal/catalog"
cat > "$TMP/internal/integration/stub.go" <<'GO'
package integration
import "context"
const(KindAdmin="admin"; KindIntegration="integration")
type Principal struct{Kind,ID string; Scopes []string}
type principalKey struct{}
func CurrentPrincipal(ctx context.Context)(Principal,bool){p,ok:=ctx.Value(principalKey{}).(Principal);return p,ok}
func WithPrincipal(ctx context.Context,p Principal)context.Context{return context.WithValue(ctx,principalKey{},p)}
func HasScope(scopes []string,want string)bool{for _,s:=range scopes{if s==want{return true}};return false}
GO
cat > "$TMP/internal/eventbus/stub.go" <<'GO'
package eventbus
import "time"
type Event struct{Topic string; Data any; Time time.Time}
type Hub struct{}
func(*Hub)Publish(Event){}
GO
cat > "$TMP/internal/channel/stub.go" <<'GO'
package channel
import("context";"io";"strings";"time")
type Direction string
const DirectionOutbound Direction="outbound"
const MetaOutboundMessageID="outbound_msg_id"
type Attachment struct{ID,Kind,Name,MIMEType,Path,URL string;Size int64;Metadata map[string]any}
type ReplyAddress struct{ChannelID,ConversationID,ThreadID,MessageID,Author string; Metadata map[string]any; ReplyCtx any}
type ChannelMessage struct{ChannelID,ConversationID,ThreadID,SourceMessageID,Author,Text string; Direction Direction; Attachments []Attachment; Metadata map[string]any; Timestamp time.Time; ReplyCtx any}
type InboundFunc func(context.Context,ChannelMessage) error
type AttachmentOpener interface{OpenAttachment(context.Context,Attachment)(io.ReadCloser,error)}
type Channel interface{Kind()string; ID()string; Start(context.Context,InboundFunc)error; Stop(context.Context)error; Send(context.Context,ChannelMessage)error}
type Card struct{Header *CardHeader; Elements []CardElement}
type CardHeader struct{Title,Color string}
type CardElement interface{cardElement();RenderText()string}
type CardMarkdown struct{Content string}; func(CardMarkdown)cardElement(){};func(m CardMarkdown)RenderText()string{return m.Content}
type CardNote struct{Text string}; func(CardNote)cardElement(){};func(n CardNote)RenderText()string{return n.Text}
type CardActions struct{Buttons [][]ButtonOption};func(CardActions)cardElement(){};func(CardActions)RenderText()string{return ""}
type CardListItem struct{Text string;Button ButtonOption};func(CardListItem)cardElement(){};func(CardListItem)RenderText()string{return ""}
type ButtonOption struct{Text,Value,Style string}
type CommandContext struct{Channel Channel;Message ChannelMessage;Hub *Hub;Command string;Args []string;Raw string}
type CommandCardHandler func(context.Context,CommandContext)(*Card,error)
type Command struct{Name,Description,Source string;CardHandler CommandCardHandler}
type Hub struct{}
func(*Hub)RegisterCommand(Command){}
func ParseCommand(text string)(string,[]string,bool){if strings.HasPrefix(strings.TrimSpace(text),"/"){return "x",nil,true};return "",nil,false}
type DispatchStatus string
const(DispatchNotHandled DispatchStatus="not_handled";DispatchHandled DispatchStatus="handled")
type InboundDispatchRequest struct{PersistedMessageID int64;Channel Channel;Message ChannelMessage;ReplyAddress ReplyAddress}
type InboundDispatchResult struct{Status DispatchStatus;Handler string}
type OutboundDeliveryService interface{Deliver(context.Context,ChannelMessage,*Card)(ChannelMessage,error)}
GO
cat > "$TMP/internal/catalog/stub.go" <<'GO'
package catalog
import "context"
type Manifest struct{ID,DisplayName,Executable string}
type Provider struct{Manifest Manifest;Enabled bool}
type RuntimeInfo struct{Installed bool;Path,InstalledVersion,VersionError string}
type Prober struct{}
func NewProber()*Prober{return &Prober{}}
func(*Prober)Installed(context.Context,Manifest)RuntimeInfo{return RuntimeInfo{}}
type Catalog struct{}
func(*Catalog)Get(context.Context,string)(Provider,error){return Provider{},nil}
GO
(
 cd "$TMP"
 GOTOOLCHAIN=local GOPROXY=off go vet ./internal/oneshot/api ./internal/oneshot/channeladapter ./internal/oneshot/notification ./internal/oneshot/appwire
 GOTOOLCHAIN=local GOPROXY=off go test -race -cover ./internal/oneshot/api ./internal/oneshot/channeladapter ./internal/oneshot/notification ./internal/oneshot/appwire
)
printf 'OD-OS-18/19/20 isolated compile gate: PASS\n'
