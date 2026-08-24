// Package iouring implements engine/transport.Transport over persistent
// per-peer TCP connections using a hand-rolled binary wire protocol
// (wire.go, frame.go), driven asynchronously by io_uring (infra/ioruntime).
//
// # Architecture
//
//   - tcpConn: Base socket stream management and binary frame reassembly.
//   - Client: Outbound RPC multiplexer tracking correlation IDs and timeouts.
//   - Server: Inbound TCP listener delivering framed messages via OnMessage.
//
// # Event Hooks
//
// Both Client and Server expose symmetrical lifecycle and message hooks:
//   - OnConnected / OnDisconnected / OnConnectError
//   - OnMessage(peerID/connFD, hdr, body)
//
// # Ownership Contract: HandleCompletion
//
// Client and Server do not run event loops. Instead, their owner (the
// reactor.Reactor) polls completions and dispatches them:
//
//	client := iouring.NewClient(runtime, reactor)
//	server := iouring.NewServer(runtime)
//	server.OnMessage = func(connFD int, hdr iouring.FrameHeader, body []byte) { ... }
//	_ = server.Listen(addr)
//
//	reactor.SetEventHandler(func(ev reactor.Event) {
//		if client.HandleCompletion(ev) {
//			return
//		}
//		server.HandleCompletion(ev)
//	})
//	go reactor.Run()
package iouring
