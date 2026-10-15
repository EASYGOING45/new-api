// Portions derived from yetone/magpie, commit 07381545205b5f47ab448a1c6a307ee86c279fa8.
// Copyright (c) 2026 yetone. MIT license; see LICENSE.magpie.
// Local experimental Cursor protocol implementation; not an official Cursor API.
package cursor

import (
	"bufio"
	"context"
	"crypto/sha256"
	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"io"
	"math"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

const cursorCall = "CallDynamicTool"

func cursorUUID() string           { return uuid.NewString() }
func cursorBlobID(b []byte) []byte { h := sha256.Sum256(b); return h[:] }
func buildCursorRun(msgs [][]byte, lastUser string, tools []bridgeTool, selection modelSelection, conv string) ([]byte, map[string][]byte) {
	if conv == "" {
		conv = cursorUUID()
	}
	blobs := map[string][]byte{}
	put := func(b []byte) []byte {
		id := cursorBlobID(b)
		blobs[string(id)] = b
		return id
	}
	var state pb
	for _, m := range msgs {
		state = state.bytes(1, put(m))
	}
	mid := cursorUUID()
	user := pb{}.str(1, lastUser).str(2, mid).varint(4, 1) // mode: agent
	turn := pb{}.bytes(1, pb{}.bytes(1, put(user)).str(10, mid))
	state = state.bytes(8, put(turn)).varint(10, 1).str(22, "cli")

	var defs []pb
	for _, t := range tools {
		defs = append(defs, cursorToolDef(t))
	}
	env := pb{}.str(1, runtime.GOOS).str(2, "/workspace").str(10, "UTC")
	rc := pb{}.bytes(4, env)
	var mcp pb
	for _, d := range defs {
		rc = rc.bytes(7, d)
		mcp = mcp.bytes(1, d)
	}
	action := pb{}.bytes(2, pb{}.bytes(2, rc)) // resume_action
	details, requested := pb{}.str(1, selection.ID).str(3, selection.ID).str(4, selection.ID), pb{}.str(1, selection.ID)
	if selection.MaxMode {
		details, requested = details.varint(7, 1), requested.varint(2, 1)
	}
	keys := make([]string, 0, len(selection.Parameters))
	for key := range selection.Parameters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		requested = requested.bytes(3, pb{}.str(1, key).str(2, selection.Parameters[key]))
	}
	if _, ok := selection.Parameters["priority_tier"]; !ok {
		requested = requested.bytes(3, pb{}.str(1, "priority_tier").str(2, "default"))
	}
	rr := pb{}.bytes(1, state).bytes(2, action).
		bytes(3, details).
		bytes(4, mcp).str(5, conv).
		bytes(9, requested).
		varint(19, 1) // inline images
	return pb{}.bytes(1, rr), blobs
}

func cursorToolDef(t bridgeTool) pb {
	d := pb{}.str(1, t.Name)
	if t.Description != "" {
		d = d.str(2, t.Description)
	}
	var schema any
	if common.Unmarshal(t.InputSchema, &schema) == nil {
		d = d.bytes(3, pbValue(schema))
	}
	return d.str(4, "magpie").str(5, t.Name).str(6, string(t.InputSchema))
}

// pbValue is a google.protobuf.Value.
func pbValue(v any) pb {
	switch x := v.(type) {
	case nil:
		return pb{}.varint(1, 0)
	case float64:
		return pb{}.double(2, x)
	case string:
		return pb{}.str(3, x)
	case bool:
		n := uint64(0)
		if x {
			n = 1
		}
		return pb{}.varint(4, n)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		var st pb
		for _, k := range keys {
			st = st.bytes(1, pb{}.str(1, k).bytes(2, pbValue(x[k])))
		}
		return pb{}.bytes(5, st)
	case []any:
		var l pb
		for _, e := range x {
			l = l.bytes(1, pbValue(e))
		}
		return pb{}.bytes(6, l)
	}
	return pb{}.varint(1, 0)
}

// pbAny reads a google.protobuf.Value.
func pbAny(b []byte) any { return pbAnyDepth(b, 0) }
func pbAnyDepth(b []byte, depth int) any {
	if depth > 32 {
		return nil
	}
	for _, f := range pbFields(b) {
		switch f.num {
		case 1:
			return nil
		case 2:
			return math.Float64frombits(f.n)
		case 3:
			return string(f.data)
		case 4:
			return f.n != 0
		case 5:
			m := map[string]any{}
			for _, e := range pbFields(f.data) {
				if e.num != 1 {
					continue
				}
				var k string
				var v any
				for _, kv := range pbFields(e.data) {
					switch kv.num {
					case 1:
						k = string(kv.data)
					case 2:
						v = pbAnyDepth(kv.data, depth+1)
					}
				}
				m[k] = v
			}
			return m
		case 6:
			l := []any{}
			for _, e := range pbFields(f.data) {
				if e.num == 1 {
					l = append(l, pbAnyDepth(e.data, depth+1))
				}
			}
			return l
		}
	}
	return nil
}

// ---- the stream ----------------------------------------------------------

// cursorStream is one Run: what the client sends, and the blobs the server
// may ask for.
type cursorStream struct {
	mu    sync.Mutex
	pw    *io.PipeWriter
	blobs map[string][]byte
}

func (st *cursorStream) send(msg []byte) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pw.Write(connectFrame(msg))
}

// heartbeat tells the server the client is still there, as the CLI does.
func (st *cursorStream) heartbeat(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st.send(pb{}.bytes(7, nil))
		}
	}
}

// closeExec ends the server's request id, as every answer to one is.
func (st *cursorStream) closeExec(id uint64) {
	st.send(pb{}.bytes(5, pb{}.bytes(1, pb{}.varint(1, id))))
}

// decode follows the server's messages into events until the turn ends, or
// the model has made its tool calls.
func (st *cursorStream) decode(ctx context.Context, br *bufio.Reader, out chan<- Event, tools []bridgeTool) {
	defer close(out)
	send := func(ev Event) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	fail := func(status int, msg string) {
		send(Event{Kind: KError, Status: status, Text: msg})
	}
	var calls, listed, written int
	var usage Usage
	finish := func() {
		// Thinking alone is not a delivered answer. In particular, turn-ended
		// usage does not prove that the model produced text or a tool call.
		if written == 0 && calls == 0 {
			send(Event{Kind: KError, Status: 502, ErrorCode: "cursor_empty_response", Text: "Cursor upstream completed without response text or tool calls"})
			return
		}
		send(Event{Kind: KUsage, Usage: usage})
		stop := "stop"
		if calls > 0 {
			stop = "tool"
		}
		send(Event{Kind: KStop, Stop: stop})
	}
	for {
		f, err := readConnectFrame(br)
		if err != nil {
			if ctx.Err() == nil {
				fail(502, "Cursor stream ended before completion")
			}
			return
		}
		if f.end {
			if status, msg := cursorFailure(200, f.data); status/100 != 2 {
				fail(status, msg)
			} else {
				finish()
			}
			return
		}
		for _, m := range pbFields(f.data) {
			switch m.num {
			case 1: // interaction_update
				for _, u := range pbFields(m.data) {
					uf := pbFields(u.data)
					switch u.num {
					case 1: // text
						if t := pbStr(uf, 1); t != "" {
							written += len(t)
							if !send(Event{Kind: KText, Text: t}) {
								return
							}
						}
					case 4: // thinking
						if t := pbStr(uf, 1); t != "" {
							if !send(Event{Kind: KThink, Text: t}) {
								return
							}
						}
					case 14: // turn ended, with what it used
						var err error
						usage, err = cursorUsage(uf)
						if err != nil {
							fail(502, "invalid Cursor token usage")
							return
						}
						if calls == 0 {
							finish()
							return
						}
					case 27: // how many tool calls the step makes, sent before them
						if n := pbNum(uf, 1); n > 128 {
							fail(502, "too many Cursor tool calls")
							return
						} else {
							listed = int(n)
						}
						if listed > 0 && calls >= listed {
							finish()
							return
						}
					}
				}
			case 2: // exec_server_message: the server asks the client to run something
				if !st.exec(m.data, tools, func(id, name string, args []byte) bool {
					calls++
					return send(Event{Kind: KToolStart, ID: id, Name: name}) && send(Event{Kind: KToolArgs, Text: string(args)})
				}) {
					return
				}
				if listed > 0 && calls >= listed { // every call made: the caller runs them
					finish()
					return
				}
			case 4: // kv_server_message: a blob wanted, or one to keep
				kv := pbFields(m.data)
				id := pbNum(kv, 1)
				for _, k := range kv {
					switch k.num {
					case 2:
						res := pb{}
						if b, ok := st.blobs[pbStr(pbFields(k.data), 1)]; ok {
							res = res.bytes(1, b)
						} else {
							res = res.bytes(2, pb{}.str(1, "blob not found"))
						}
						st.send(pb{}.bytes(3, pb{}.varint(1, id).bytes(2, res)))
					case 3: // the server's own record of the turn, not needed
						st.send(pb{}.bytes(3, pb{}.varint(1, id).bytes(3, nil)))
					}
				}
			}
		}
	}
}

// exec answers what the server asks the client to run: a call of the
// caller's tools goes to call, the list of them is given, and anything
// else is refused. It is false once the caller has gone.
func (st *cursorStream) exec(msg []byte, tools []bridgeTool, call func(id, name string, args []byte) bool) bool {
	es := pbFields(msg)
	id := pbNum(es, 1)
	execID := pbStr(es, 15)
	answer := func(num int, result pb) {
		st.send(pb{}.bytes(2, pb{}.varint(1, id).str(15, execID).bytes(num, result)))
		st.closeExec(id)
	}
	for _, e := range es {
		switch e.num {
		case 11: // mcp_args: a call of the caller's tools
			a := pbFields(e.data)
			args := map[string]any{}
			for _, kv := range a {
				if kv.num != 2 {
					continue
				}
				ef := pbFields(kv.data)
				var v any
				for _, x := range ef {
					if x.num == 2 {
						v = pbAny(x.data)
					}
				}
				args[pbStr(ef, 1)] = v
			}
			js, _ := common.Marshal(args)
			name := pbStr(a, 5)
			if name == "" {
				name = strings.TrimPrefix(pbStr(a, 1), "magpie-")
			}
			// an OpenAI model's id is its call's and its item's, a line apart,
			// which no caller would take as an id: the line goes as __
			callID := strings.ReplaceAll(pbStr(a, 3), "\n", "__")
			if callID == "" {
				callID = "call_" + cursorUUID()
			}
			if !slices.ContainsFunc(tools, func(t bridgeTool) bool { return t.Name == name }) {
				st.send(pb{}.bytes(5, pb{}.bytes(2, pb{}.varint(1, id).str(2, "undeclared tool"))))
				st.closeExec(id)
				return true
			}
			return call(callID, name, js)
		case 36: // mcp_state_exec_args: the tools there are
			srv := pb{}.str(1, "magpie").str(2, "magpie").str(7, "connected")
			for _, t := range tools {
				srv = srv.bytes(5, cursorToolDef(t))
			}
			answer(36, pb{}.bytes(1, pb{}.bytes(1, srv)))
			return true
		case 10: // request_context_args
			env := pb{}.str(1, runtime.GOOS).str(2, "/workspace").str(10, "UTC")
			answer(10, pb{}.bytes(1, pb{}.bytes(1, pb{}.bytes(4, env))))
			return true
		}
	}
	// a shell, a file read or an edit of Cursor's own: never run
	st.send(pb{}.bytes(5, pb{}.bytes(2, pb{}.varint(1, id).str(2, "not available"))))
	st.closeExec(id)
	return true
}

// pbStr and pbNum are a field of a message read, "" or 0 when it has none.
func pbStr(fs []pbField, num int) string {
	for _, f := range fs {
		if f.num == num {
			return string(f.data)
		}
	}
	return ""
}

func pbNum(fs []pbField, num int) uint64 {
	for _, f := range fs {
		if f.num == num {
			return f.n
		}
	}
	return 0
}
