package main

// ============================================================================
// pulse — a tiny Tina + Datastar demo
//
// One Counter isolate lives on one shard. Every browser that opens the page
// subscribes to it over Server-Sent Events (Datastar's protocol) and sees the
// same count, the same uptime, the same generation number.
//
//   +1                 -> POST /bump  -> message TAG_BUMP  -> Counter.count += 1 -> broadcast
//   crash the counter  -> POST /crash -> message TAG_CRASH -> Counter panics
//
// When the Counter crashes, its supervisor restarts it from the boot spec. It
// comes back with a new handle and empty state, so the count resets and the
// generation number goes up. The HTTP connections notice (their parking window
// elapses), re-subscribe to the new Counter, and the page keeps going. Nothing
// else on the shard is affected; the HTTP listener never stopped.
//
// Build & run:   odin run . -out:pulse      then open http://127.0.0.1:8080
// ============================================================================

import tina "../tina/src"
import datastar "../tina/src/extensions/http/datastar"
import http "../tina/src/extensions/http/server"
import "core:fmt"

TICK_NS :: u64(1_000_000_000)
PARK_NS :: 2 * TICK_NS // how long a connection waits for a feed before re-subscribing
SUBSCRIBERS_MAX :: 256

TAG_TICK: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 1
TAG_SUBSCRIBE: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 2
TAG_UNSUBSCRIBE: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 3
TAG_FEED: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 4
TAG_BUMP: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 5
TAG_CRASH: tina.Message_Tag : tina.USER_MESSAGE_TAG_BASE + 6

// Handle of the one Counter. Rewritten by counter_init on every (re)start, so
// routes always talk to the live generation. HANDLE_NONE until first boot.
counter_handle: tina.Isolate_Handle = tina.ISOLATE_HANDLE_NONE

// Process-global, so it survives isolate restarts: how many times the Counter
// has booted. Isolate state itself is wiped on restart.
boot_count: u32 = 0

// ─── Messages ───────────────────────────────────────────────────────────────

Sub_Msg :: struct {
	request_token: http.Request_Token,
}

Feed_Msg :: struct {
	request_token: http.Request_Token,
	count:         u32,
	ticks:         u32,
	generation:    u32,
}

// ─── The Counter isolate ────────────────────────────────────────────────────

Subscriber :: struct {
	handle:        tina.Isolate_Handle,
	request_token: http.Request_Token,
}

Counter :: struct {
	subscribers:      [SUBSCRIBERS_MAX]Subscriber,
	subscriber_count: u32,
	count:            u32,
	ticks:            u32,
	generation:       u32,
}

counter_init :: proc(self_raw: rawptr, args: []u8) -> tina.Isolate_Transition {
	self := tina.self_as(Counter, self_raw)
	boot_count += 1
	self.subscriber_count = 0
	self.count = 0
	self.ticks = 0
	self.generation = boot_count
	counter_handle = tina.ctx_self_handle()
	tina.ctx_register_timer(TICK_NS, TAG_TICK)
	return tina.ISOLATE_TRANSITION_WAIT_MESSAGE
}

counter_handler :: proc(self_raw: rawptr, message: ^tina.Message) -> tina.Isolate_Transition {
	self := tina.self_as(Counter, self_raw)

	switch message.tag {
	case TAG_SUBSCRIBE:
		if self.subscriber_count >= SUBSCRIBERS_MAX {
			return tina.ISOLATE_TRANSITION_WAIT_MESSAGE // shed: the connection re-subscribes later
		}
		msg := tina.payload_as(Sub_Msg, message.user.payload[:])
		self.subscribers[self.subscriber_count] = Subscriber {
			handle        = message.user.source,
			request_token = msg.request_token,
		}
		self.subscriber_count += 1
		counter_broadcast(self) // greet the newcomer with the current numbers

	case TAG_UNSUBSCRIBE:
		for index := u32(0); index < self.subscriber_count; index += 1 {
			if self.subscribers[index].handle == message.user.source {
				self.subscriber_count -= 1
				self.subscribers[index] = self.subscribers[self.subscriber_count]
				break
			}
		}

	case TAG_BUMP:
		self.count += 1
		counter_broadcast(self)

	case TAG_CRASH:
		line := fmt.bprintf(
			tina.ctx_scratch_arena_bytes(),
			"[FAIL] Counter generation %d crashing on request (count was %d)",
			self.generation,
			self.count,
		)
		tina.ctx_log(.ERROR, tina.USER_LOG_TAG_BASE, transmute([]u8)line)
		// The trap boundary catches this; the supervisor rebuilds the Counter.
		return tina.transition_to_crash(.None)

	case TAG_TICK:
		self.ticks += 1
		counter_broadcast(self)
		tina.ctx_register_timer(TICK_NS, TAG_TICK)
	}

	return tina.ISOLATE_TRANSITION_WAIT_MESSAGE
}

// Send the current numbers to every subscriber; forget subscribers whose
// connection isolate is gone (stale handle). A full mailbox just drops this
// update for that one slow client.
counter_broadcast :: proc(self: ^Counter) {
	write_index := u32(0)
	for read_index := u32(0); read_index < self.subscriber_count; read_index += 1 {
		subscriber := self.subscribers[read_index]
		feed := Feed_Msg {
			request_token = subscriber.request_token,
			count         = self.count,
			ticks         = self.ticks,
			generation    = self.generation,
		}
		if tina.ctx_send(subscriber.handle, TAG_FEED, &feed) == .stale_handle {
			continue
		}
		self.subscribers[write_index] = subscriber
		write_index += 1
	}
	self.subscriber_count = write_index
}

// ─── HTTP: the page ─────────────────────────────────────────────────────────

INDEX_HTML :: `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>pulse</title>
<script type="module" src="https://cdn.jsdelivr.net/gh/starfederation/datastar@v1.0.4/bundles/datastar.js"></script>
<style>
  body { font: 15px/1.5 ui-monospace, monospace; background: #0f1115; color: #d7dae0; margin: 0; padding: 2rem 1rem; }
  main { max-width: 40rem; margin: 0 auto; }
  h1 { font-size: 1.25rem; margin: 0; color: #7cc8ff; }
  p.sub { color: #8b93a3; margin: .25rem 0 1.5rem; }
  .big { font-size: 6rem; line-height: 1; font-weight: 700; margin: 1rem 0 .5rem; }
  .meta { color: #8b93a3; margin-bottom: 1.5rem; }
  .meta b { color: #d7dae0; font-weight: normal; }
  .row { display: flex; gap: .75rem; margin-bottom: 1.5rem; }
  button { font: inherit; padding: .6rem 1.1rem; border-radius: 6px; border: 1px solid #335; background: #1b2235; color: #d7dae0; cursor: pointer; }
  button:hover { border-color: #7cc8ff; }
  button.danger { border-color: #533; background: #2a1b1b; }
  button.danger:hover { border-color: #ff7c7c; }
  #log { border-top: 1px solid #262a33; padding-top: 1rem; color: #8b93a3; font-size: 13px; }
  #log .warn { color: #ffb86c; }
</style>
</head>
<body data-signals="{count: 0, ticks: 0, generation: 0, live: false}" data-init="@get('/events')">
<main>
  <h1>pulse</h1>
  <p class="sub">One Counter isolate on one Tina shard. Every browser on this page subscribes to it and sees the same numbers. Open it twice.</p>
  <div class="big" data-text="$count"></div>
  <div class="meta">
    generation <b data-text="$generation"></b> &middot;
    up <b data-text="$ticks"></b>s &middot;
    <b data-text="$live ? 'live' : 'connecting'"></b>
  </div>
  <div class="row">
    <button data-on:click="@post('/bump')">+1</button>
    <button class="danger" data-on:click="@post('/crash')">crash the counter</button>
  </div>
  <div id="log"></div>
</main>
</body>
</html>
`

index :: proc(request: ^http.Request, response: ^http.Response) -> http.Route_Step {
	return http.respond_bytes(
		response,
		http.HTTP_STATUS_OK,
		"text/html; charset=utf-8",
		transmute([]u8)string(INDEX_HTML),
	)
}

// ─── HTTP: the feed (GET /events) ───────────────────────────────────────────

Events_State :: struct {
	subscribed:      bool,
	seen_any:        bool,
	last_generation: u32,
}

events :: proc(
	event: http.Route_Event,
	request: ^http.Request,
	response: ^http.Response,
	route_context: http.Route_Context,
	state: rawptr,
) -> http.Route_Step {
	st := cast(^Events_State)state

	switch ev in event {
	case http.Request_Start:
		st^ = Events_State{}
		if counter_handle == tina.ISOLATE_HANDLE_NONE {
			return http.respond_text(response, http.HTTP_STATUS_SERVICE_UNAVAILABLE, "counter not ready")
		}
		sse, start_error := datastar.start_sse(response)
		if start_error != .None do return http.close()
		if datastar.patch_signals(&sse, `{"live": true}`) != .None do return http.close()
		return http.flush()

	case http.Send_Ready:
		return subscribe_and_park(st, route_context)

	case http.Application_Notification:
		feed := (cast(^Feed_Msg)raw_data(ev.payload_bytes))^
		if feed.request_token != http.route_request_token(route_context) {
			return park(route_context) // a notification meant for an earlier request on this connection
		}
		sse := datastar.resume(response)
		signals := fmt.bprintf(
			make([]u8, 96, http.request_scratch(request)),
			`{{"count": %d, "ticks": %d, "generation": %d}}`,
			feed.count,
			feed.ticks,
			feed.generation,
		)
		if datastar.patch_signals(&sse, signals) != .None do return http.close()
		if st.seen_any && feed.generation != st.last_generation {
			line := fmt.bprintf(
				make([]u8, 160, http.request_scratch(request)),
				`<div class="warn">counter crashed; supervisor restarted it as generation %d (count reset, new handle)</div>`,
				feed.generation,
			)
			if datastar.patch_elements(
				   &sse,
				   line,
				   datastar.Patch_Elements_Options{selector = "#log", mode = .Append},
			   ) !=
			   .None {
				return http.close()
			}
		}
		st.seen_any = true
		st.last_generation = feed.generation
		return http.flush()

	case http.Application_Reply:
		// Parking window elapsed with no feed: the Counter probably crashed and
		// restarted (new handle, empty subscriber list). Subscribe to the live one.
		st.subscribed = false
		return subscribe_and_park(st, route_context)

	case http.Body_Chunk:
		return http.close()

	case http.Peer_Closed, http.Server_Drain:
		return unsubscribe_and_close(route_context)
	}
	return http.close()
}

subscribe_and_park :: proc(st: ^Events_State, route_context: http.Route_Context) -> http.Route_Step {
	if !st.subscribed {
		counter := counter_handle
		if counter == tina.ISOLATE_HANDLE_NONE do return http.close()
		sub := Sub_Msg {
			request_token = http.route_request_token(route_context),
		}
		if http.route_send(route_context, counter, TAG_SUBSCRIBE, tina.bytes_of(&sub)) != .ok {
			return http.close()
		}
		st.subscribed = true
	}
	return park(route_context)
}

// Wait for the next TAG_FEED from whichever Counter generation is alive.
park :: proc(route_context: http.Route_Context) -> http.Route_Step {
	return http.expect_notification(route_context, PARK_NS, message_tag = TAG_FEED)
}

unsubscribe_and_close :: proc(route_context: http.Route_Context) -> http.Route_Step {
	if counter_handle != tina.ISOLATE_HANDLE_NONE {
		empty: [0]u8
		_ = http.route_send(route_context, counter_handle, TAG_UNSUBSCRIBE, empty[:])
	}
	return http.close()
}

// ─── HTTP: commands (POST /bump, POST /crash) ───────────────────────────────

bump :: proc(
	event: http.Route_Event,
	request: ^http.Request,
	response: ^http.Response,
	route_context: http.Route_Context,
	state: rawptr,
) -> http.Route_Step {
	return command(event, response, route_context, TAG_BUMP)
}

crash :: proc(
	event: http.Route_Event,
	request: ^http.Request,
	response: ^http.Response,
	route_context: http.Route_Context,
	state: rawptr,
) -> http.Route_Step {
	return command(event, response, route_context, TAG_CRASH)
}

// Fire one message at the Counter and answer with an (empty) Datastar stream,
// which is what @post expects back.
command :: proc(
	event: http.Route_Event,
	response: ^http.Response,
	route_context: http.Route_Context,
	$tag: tina.Message_Tag,
) -> http.Route_Step {
	#partial switch ev in event {
	case http.Request_Start:
		empty: [0]u8
		counter := counter_handle
		if counter == tina.ISOLATE_HANDLE_NONE ||
		   http.route_send(route_context, counter, tag, empty[:]) != .ok {
			return http.respond_text(response, http.HTTP_STATUS_SERVICE_UNAVAILABLE, "counter unavailable")
		}
		sse, start_error := datastar.start_sse(response)
		if start_error != .None do return http.close()
		if datastar.patch_signals(&sse, `{"live": true}`) != .None do return http.close()
		return http.flush(final = true)
	}
	return http.close()
}

// ─── Boot ───────────────────────────────────────────────────────────────────

main :: proc() {
	app := http.App {
		routes = []http.Route {
			http.get("/", index),
			http.get_event("/events", events, state_size = u16(size_of(Events_State))),
			http.post_event("/bump", bump, body_size_max = 4096, body_mode = .Buffered),
			http.post_event("/crash", crash, body_size_max = 4096, body_mode = .Buffered),
		},
	}
	server := http.Server {
		address = tina.ipv4(127, 0, 0, 1, 8080),
		app     = &app,
	}
	spec := http.install_development_defaults(&server)

	// Register the Counter as an isolate type ...
	counter_type_id := tina.Isolate_Type_Id(len(spec.types))
	types := make([]tina.IsolateTypeDescriptor, len(spec.types) + 1)
	copy(types, spec.types)
	types[counter_type_id] = tina.IsolateTypeDescriptor {
		id                = counter_type_id,
		slot_count        = 1,
		stride            = size_of(Counter),
		soa_metadata_size = size_of(tina.Isolate_Metadata),
		mailbox_capacity  = 256,
		init_handler      = counter_init,
		handler_fn        = counter_handler,
	}
	spec.types = types

	// ... and supervise it in its own group with a generous restart budget, so
	// the crash button can be pressed as often as you like without the root
	// group giving up and quarantining the shard (which would take the HTTP
	// listener with it). It goes first so it is alive before the listener.
	root := &spec.shard_specs[0].root_group
	children := make([]tina.Child_Spec, len(root.children) + 1)
	children[0] = tina.Group_Spec {
		strategy              = .One_For_One,
		restart_count_max     = 10_000,
		window_duration_ticks = 1,
		children              = []tina.Child_Spec {
			tina.Static_Child_Spec{type_id = counter_type_id, restart_type = .permanent},
		},
	}
	copy(children[1:], root.children)
	root.children = children

	fmt.println("pulse — open http://127.0.0.1:8080 (in two windows)")
	fmt.println("  POST /bump   +1        POST /crash   crash the Counter and watch it come back")
	tina.tina_start(&spec)
}
