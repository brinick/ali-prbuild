package prbuild

import (
	"context"
	"time"

	"github.com/brinick/logging"
)

// dataSender defines the methods a service should provide
type dataSender interface {
	send(interface{}) error
	sendWithContext(context.Context, interface{}) error
	Name() string
}

// channelData is the data structure that gets pushed to
// the background service channel for forwarding to the wrapped service
type channelData struct {
	ctx  context.Context
	data interface{}
}

// senderService is the "base" structure for services and collects
// stats on sending. Services should embed a senderService, and
// defer a call to updateSendCounts within their send/sendWithContext methods
// to use it.
type senderService struct {
	started    int64
	lastSend   int64
	lastSendOK int64
	nSendsOK   int
	nSendsFail int
}

func (s *senderService) updateSendCounts(e error) {
	now := time.Now().Unix()
	s.lastSend = now

	if e != nil {
		s.nSendsFail++
	} else {
		s.lastSendOK = now
		s.nSendsOK++
	}
}

// ---------------------------------------------------------

type backgroundServices map[string]*backgroundService

// ---------------------------------------------------------

// backgroundService wraps a service in a goroutine.
// Data should be pushed to the service using the send/sendWithContext
// methods, which pushes the data to the backgroundService channel.
// The goroutine pulls this data and forwards it to the wrapped service's
// sendWithContext method. A backgroundService may be shut down by a call
// to its SetAbort() method - this will terminate the goroutine and exit.
type backgroundService struct {
	// channel on which data will be retrieved
	c chan channelData

	// service to which data is sent
	svc dataSender

	// send commands should, if timeout > 0, be ended after timeout seconds
	// else have no timeout if timeout is <= 0
	timeout int

	// indicate to shutdown the service
	abort chan struct{}
}

func (bs *backgroundService) Launch() {
	go func() {
		defer func() {
			// restart if necessary
			if r := recover(); r != nil {
				Info(
					"Backgrounded service panicked. Relaunching.",
					Field("name", bs.svc.Name()),
				)
				bs.Launch()
			}
		}()

	outer:
		for {
			select {
			case <-bs.Abort():
				Info(
					"Background service received abort signal, exiting",
					Field("name", bs.svc.Name()),
				)
				break outer

			case item := <-bs.c:
				var (
					ctx     context.Context
					timeout context.CancelFunc
				)
				ctx, abort := context.WithCancel(item.ctx)
				if bs.timeout > 0 {
					ctx, timeout = context.WithTimeout(ctx, time.Duration(bs.timeout)*time.Second)
					defer timeout()
				}

				// Send the data to the service in a goroutine
				done := make(chan struct{})
				go func() {
					defer close(done)
					bs.svc.sendWithContext(ctx, item.data)
				}()

				// Wait for:
				// - the service to be aborted, or
				// - the context to be cancelled, or
				// - the service send to complete
				select {
				case <-bs.Abort():
					Info(
						"Background service received abort signal, exiting",
						Field("name", bs.svc.Name()),
					)
					abort()
					break outer

				case <-ctx.Done():
					Info(
						"Background service received abort signal, exiting",
						Field("name", bs.svc.Name()),
					)
					abort()
					break outer

				case <-done:
					abort()
					// go round again
				}
			}
		}
	}()
}

func (bs *backgroundService) SetAbort() {
	close(bs.abort)
}

func (bs *backgroundService) Abort() <-chan struct{} {
	return bs.abort
}

func (bs *backgroundService) send(data interface{}) {
	bs.sendWithContext(context.TODO(), data)
}

func (bs *backgroundService) sendWithContext(ctx context.Context, data interface{}) {
	item := channelData{
		ctx:  ctx,
		data: data,
	}

	Error("Service disabled for now, dropping data", logging.F("svc", bs.svc.Name()))
	return

	// We have to do this select to ensure a non-blocking channel send
	// in the case where the channel is full and we try and send it a value
	select {
	case bs.c <- item:
		// channel was not full, all is well
	default:
		Info("Service data channel is full, ignoring new data")
	}
}
