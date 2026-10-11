package transport

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport/outbound"
)

type screenOfferResenderFunc func(context.Context, string) error

func (f screenOfferResenderFunc) ResendScreen(ctx context.Context, sid string) error {
	return f(ctx, sid)
}

func newScreenOfferFixture(t *testing.T, resender ScreenResender, socket outbound.Socket, capture *captureSocket, cfg outbound.Config) (*WSServer, session.ID, [16]byte, *wsConn, *sessionRx, *captureSocket) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger), WithScreenResender(resender))
	if err := ws.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(context.Background()) })

	sid := session.ID("0123456789abcdef0123456789abcdef")
	sidBytes, err := session.IDToBytes(sid)
	if err != nil {
		t.Fatalf("id to bytes: %v", err)
	}
	rx := ws.getOrCreateRx(sid)
	if rx == nil {
		t.Fatal("the server would not make a ring")
	}
	wconn := &wsConn{out: outbound.New(socket, cfg), log: logger, id: 1}
	t.Cleanup(wconn.out.Close)
	rx.setSubscriber(wconn, nil)
	return ws, sid, sidBytes, wconn, rx, capture
}

func TestGapHistoryAttachResetsScreenOfferForPromptBoundary(t *testing.T) {
	const screenRevision = 7
	doc := []byte(`{"revision":7,"geometry":{"cols":80,"rows":24}}`)
	oldSocket := newCaptureSocket()
	ws, sid, sidBytes, _, rx, _ := newScreenOfferFixture(t, nil, oldSocket, oldSocket, outbound.Config{})

	newSocket := newCaptureSocket()
	newConn := &wsConn{out: outbound.New(newSocket, outbound.Config{}), log: ws.log, id: 2}
	t.Cleanup(newConn.out.Close)
	if _, _, ok := rx.setSubscriberIfGapHistoryAvailable(newConn, nil); !ok {
		t.Fatal("reattach was refused despite complete output-gap history")
	}
	if !ws.PublishScreenFrame(sid, screenRevision, doc) {
		t.Fatal("prompt screen was not accepted for the reattached subscriber")
	}
	_, _, offered, _, _, _, ok := rx.screenOfferSnapshot(newConn)
	if !ok || offered < screenRevision {
		t.Fatalf("screen offer state after reattach = (offered %d, ok %v), want revision %d for the new subscriber", offered, ok, screenRevision)
	}
	if !ws.PublishSessionEffect(sid, proto.EffectFrame{
		Generation: 2, EffectID: 9, Kind: proto.EffectPromptBoundary,
		StreamOffset: 0, ScreenRevision: screenRevision,
	}) {
		t.Fatal("prompt boundary was not accepted")
	}

	ctx, cancel := context.WithCancel(t.Context())
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		ws.ringToConn(ctx, newConn, sidBytes, rx, 0)
	}()
	defer func() {
		cancel()
		<-pumpDone
	}()
	frames := awaitCapturedFrames(t, newSocket, 2)
	if frames[0].MsgType != websocket.BinaryMessage || frames[1].MsgType != websocket.TextMessage {
		t.Fatalf("reattached screen/effect frame types = %d, %d; want screen then prompt effect", frames[0].MsgType, frames[1].MsgType)
	}
	var effect struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(frames[1].Data, &effect); err != nil || effect.Method != "session.effect" {
		t.Fatalf("reattached prompt effect = %s, decode err=%v, want session.effect", frames[1].Data, err)
	}
}

func TestEffectWakeBroadcastsAcrossSubscriberReplacement(t *testing.T) {
	oldSocket := newCaptureSocket()
	_, _, _, _, rx, _ := newScreenOfferFixture(t, nil, oldSocket, oldSocket, outbound.Config{})
	oldWake := rx.effectWake

	newSocket := newCaptureSocket()
	newConn := &wsConn{out: outbound.New(newSocket, outbound.Config{}), log: log.NewSlogAdapter(nil), id: 2}
	t.Cleanup(newConn.out.Close)
	rx.setSubscriber(newConn, nil)
	newWake := rx.effectWake
	if oldWake != newWake {
		t.Fatal("subscriber replacement must keep one broadcast wake generation")
	}

	rx.signalEffectWake()
	for i, wake := range []<-chan struct{}{oldWake, newWake} {
		select {
		case <-wake:
		default:
			t.Fatalf("subscriber pump %d did not observe the effect wake", i)
		}
	}
}

func TestPromptBoundaryWaitsForItsScreenRevisionBeforeSendingEffect(t *testing.T) {
	const screenRevision = 7
	doc := []byte(`{"revision":7,"geometry":{"cols":80,"rows":24}}`)
	var ws *WSServer
	resender := screenOfferResenderFunc(func(ctx context.Context, sid string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !ws.PublishScreenFrame(session.ID(sid), screenRevision, doc) {
			return errors.New("screen frame was not accepted")
		}
		return nil
	})
	sock := newCaptureSocket()
	ws, sid, sidBytes, wconn, rx, _ := newScreenOfferFixture(t, resender, sock, sock, outbound.Config{})
	ctx, cancel := context.WithCancel(t.Context())
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		ws.ringToConn(ctx, wconn, sidBytes, rx, 0)
	}()
	t.Cleanup(func() {
		cancel()
		<-pumpDone
	})

	prefix := []byte("prompt-prefix")
	if err := rx.ring.write(prefix); err != nil {
		t.Fatalf("write prompt prefix: %v", err)
	}
	awaitCapturedFrames(t, sock, 1)
	if !ws.PublishSessionEffect(sid, proto.EffectFrame{
		Generation: 2, EffectID: 9, Kind: proto.EffectPromptBoundary,
		StreamOffset: uint64(len(prefix)), ScreenRevision: screenRevision,
	}) {
		t.Fatal("prompt boundary was not accepted")
	}

	frames := awaitCapturedFrames(t, sock, 2)
	if frames[1].MsgType != websocket.BinaryMessage {
		t.Fatalf("frame after prompt prefix = type %d, want matching screen frame before the effect", frames[1].MsgType)
	}
	screen, err := DecodeFrame(frames[1].Data)
	if err != nil {
		t.Fatalf("decode screen frame: %v", err)
	}
	if screen.MsgType != MsgTypeMetadata || string(screen.Payload) != string(doc) {
		t.Fatalf("frame after prompt prefix = type %#x payload %q, want matching screen revision %d", screen.MsgType, screen.Payload, screenRevision)
	}
	frames = awaitCapturedFrames(t, sock, 3)
	var effect struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(frames[2].Data, &effect); err != nil {
		t.Fatalf("decode effect notification: %v", err)
	}
	if frames[2].MsgType != websocket.TextMessage || effect.Method != "session.effect" {
		t.Fatalf("frame after matching screen = type %d method %q, want session.effect", frames[2].MsgType, effect.Method)
	}
}

func TestPromptBoundaryIsDroppedWhenItsCurrentScreenCannotBeOffered(t *testing.T) {
	const screenRevision = 8
	started := make(chan struct{}, 1)
	resender := screenOfferResenderFunc(func(context.Context, string) error {
		started <- struct{}{}
		return errors.New("helper screen is unavailable")
	})
	sock := newCaptureSocket()
	ws, sid, _, wconn, rx, _ := newScreenOfferFixture(t, resender, sock, sock, outbound.Config{})
	if !ws.PublishSessionEffect(sid, proto.EffectFrame{
		Generation: 2, EffectID: 15, Kind: proto.EffectPromptBoundary, ScreenRevision: screenRevision,
	}) {
		t.Fatal("prompt boundary was not accepted into the bounded offer path")
	}
	awaitScreenOfferSignal(t, started, "the screen resend attempt")
	for {
		_, _, _, failed, _, wake, ok := rx.screenOfferSnapshot(wconn)
		if !ok {
			t.Fatal("subscriber offer state disappeared before the resend failure was recorded")
		}
		if failed >= screenRevision {
			break
		}
		<-wake
	}
	if !rx.enqueueDueEffects(wconn, 0) {
		t.Fatal("failed screen offer left a blocked effect")
	}
	rx.deliveryMu.Lock()
	defer rx.deliveryMu.Unlock()
	if len(rx.pendingEffects) != 0 {
		t.Fatalf("pending effects after unofferable screen = %d, want dropped at-most-once boundary", len(rx.pendingEffects))
	}
	sock.mu.Lock()
	defer sock.mu.Unlock()
	if len(sock.frames) != 0 {
		t.Fatalf("an effect was sent after its screen offer failed: %d frames", len(sock.frames))
	}
}

type screenOfferGatedSocket struct {
	*captureSocket
	writeStarted chan struct{}
	released     chan struct{}
	writeOnce    sync.Once
	releaseOnce  sync.Once
}

func (s *screenOfferGatedSocket) WriteMessage(msgType int, data []byte) error {
	s.writeOnce.Do(func() {
		close(s.writeStarted)
		<-s.released
	})
	return s.captureSocket.WriteMessage(msgType, data)
}

func (s *screenOfferGatedSocket) release() {
	s.releaseOnce.Do(func() { close(s.released) })
}

func awaitScreenOfferSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func awaitScreenOfferAttempt(t *testing.T, attempts <-chan bool, label string) bool {
	t.Helper()
	select {
	case accepted := <-attempts:
		return accepted
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return false
	}
}

func TestPromptBoundaryOfferRetriesAfterOutboundBackpressure(t *testing.T) {
	const screenRevision = 11
	doc := []byte(`{"revision":11,"geometry":{"cols":80,"rows":24}}`)
	gate := &screenOfferGatedSocket{
		captureSocket: newCaptureSocket(),
		writeStarted:  make(chan struct{}),
		released:      make(chan struct{}),
	}
	attempts := make(chan bool, 4)
	var ws *WSServer
	resender := screenOfferResenderFunc(func(ctx context.Context, sid string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		attempts <- ws.PublishScreenFrame(session.ID(sid), screenRevision, doc)
		return nil
	})
	ws, sid, sidBytes, wconn, rx, sock := newScreenOfferFixture(t, resender, gate, gate.captureSocket, outbound.Config{QueueDepth: 1})
	t.Cleanup(gate.release)

	ctx, cancel := context.WithCancel(t.Context())
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		ws.ringToConn(ctx, wconn, sidBytes, rx, 0)
	}()
	t.Cleanup(func() {
		cancel()
		<-pumpDone
	})

	if err := wconn.out.TryEnqueue(websocket.TextMessage, []byte("in-flight")); err != nil {
		t.Fatalf("enqueue blocked frame: %v", err)
	}
	awaitScreenOfferSignal(t, gate.writeStarted, "the outbound socket write")
	if err := wconn.out.TryEnqueue(websocket.TextMessage, []byte("queued")); err != nil {
		t.Fatalf("fill outbound queue: %v", err)
	}
	if !ws.PublishSessionEffect(sid, proto.EffectFrame{
		Generation: 2, EffectID: 12, Kind: proto.EffectPromptBoundary,
		StreamOffset: 1, ScreenRevision: screenRevision,
	}) {
		t.Fatal("prompt boundary was not accepted")
	}

	if accepted := awaitScreenOfferAttempt(t, attempts, "the first screen offer"); accepted {
		t.Fatal("screen frame was accepted while the outbound queue was full")
	}
	gate.release()
	if accepted := awaitScreenOfferAttempt(t, attempts, "the retry after outbound capacity returned"); !accepted {
		t.Fatal("screen frame was still refused after outbound capacity returned")
	}
	if err := rx.ring.write([]byte("x")); err != nil {
		t.Fatalf("write prompt-boundary byte: %v", err)
	}
	awaitScreenBeforeEffect(t, sock, doc)
}

func TestPendingScreenOfferIsCancelledWhenSubscriberIsReplaced(t *testing.T) {
	started := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	resender := screenOfferResenderFunc(func(ctx context.Context, _ string) error {
		started <- struct{}{}
		<-ctx.Done()
		cancelled <- struct{}{}
		return ctx.Err()
	})
	capture := newCaptureSocket()
	ws, sid, _, oldConn, rx, _ := newScreenOfferFixture(t, resender, capture, capture, outbound.Config{})
	if !ws.PublishSessionEffect(sid, proto.EffectFrame{
		Generation: 2, EffectID: 13, Kind: proto.EffectPromptBoundary, ScreenRevision: 5,
	}) {
		t.Fatal("prompt boundary was not accepted")
	}
	awaitScreenOfferSignal(t, started, "the old subscriber's screen offer")

	logger := log.NewSlogAdapter(nil)
	newCapture := newCaptureSocket()
	newConn := &wsConn{out: outbound.New(newCapture, outbound.Config{}), log: logger, id: 2}
	t.Cleanup(newConn.out.Close)
	previous, _ := rx.setSubscriber(newConn, nil)
	if previous != oldConn {
		t.Fatalf("replaced subscriber = %p, want %p", previous, oldConn)
	}
	awaitScreenOfferSignal(t, cancelled, "cancellation of the replaced subscriber's offer")

	rx.deliveryMu.Lock()
	pending := len(rx.pendingEffects)
	rx.deliveryMu.Unlock()
	if pending != 0 {
		t.Fatalf("replacement retained %d effects from the old subscriber", pending)
	}
}

func TestScreenOfferIsCancelledWhenServerStops(t *testing.T) {
	started := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	resender := screenOfferResenderFunc(func(ctx context.Context, _ string) error {
		started <- struct{}{}
		<-ctx.Done()
		cancelled <- struct{}{}
		return ctx.Err()
	})
	capture := newCaptureSocket()
	ws, sid, _, _, _, _ := newScreenOfferFixture(t, resender, capture, capture, outbound.Config{})
	if !ws.PublishSessionEffect(sid, proto.EffectFrame{
		Generation: 2, EffectID: 14, Kind: proto.EffectPromptBoundary, ScreenRevision: 6,
	}) {
		t.Fatal("prompt boundary was not accepted")
	}
	awaitScreenOfferSignal(t, started, "the screen offer before shutdown")

	stopDone := make(chan error, 1)
	go func() { stopDone <- ws.Stop(context.Background()) }()
	awaitScreenOfferSignal(t, cancelled, "screen offer cancellation during shutdown")
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server Stop did not join the cancelled screen offer")
	}
}

func awaitScreenBeforeEffect(t *testing.T, sock *captureSocket, wantDoc []byte) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	timer := time.AfterFunc(time.Until(deadline), func() {
		sock.mu.Lock()
		sock.cond.Broadcast()
		sock.mu.Unlock()
	})
	defer timer.Stop()

	sock.mu.Lock()
	defer sock.mu.Unlock()
	for {
		screenIndex, effectIndex := -1, -1
		for i, frame := range sock.frames {
			if frame.MsgType == websocket.BinaryMessage {
				decoded, err := DecodeFrame(frame.Data)
				if err == nil && decoded.MsgType == MsgTypeMetadata && string(decoded.Payload) == string(wantDoc) {
					screenIndex = i
				}
			}
			if frame.MsgType == websocket.TextMessage {
				var msg struct {
					Method string `json:"method"`
				}
				if json.Unmarshal(frame.Data, &msg) == nil && msg.Method == "session.effect" {
					effectIndex = i
				}
			}
		}
		if effectIndex >= 0 && (screenIndex < 0 || effectIndex < screenIndex) {
			t.Fatalf("session.effect reached the socket at %d before matching screen frame at %d", effectIndex, screenIndex)
		}
		if screenIndex >= 0 && effectIndex > screenIndex {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatal("matching screen frame and prompt effect did not both reach the socket")
		}
		sock.cond.Wait()
	}
}
