package prbuild

import (
	"context"
	"fmt"
	"net"
	"time"
)

type monalisaData struct {
	name  string
	path  string
	value interface{}
}

// prChecker returns a monalisaData instance with
// the path attribute pre-filled
func (m *monalisaData) prChecker(name string, value interface{}) *monalisaData {
	return &monalisaData{
		name:  name,
		value: value,
		path: fmt.Sprintf(
			"github-pr-checker.%s_Nodes/%s",
			cfg.ciName,
			cfg.service.analytics.id,
		),
	}
}

// ---------------------------------------------------------

func newMonalisaSvc(host string, port int) *monalisaSvc {
	return &monalisaSvc{
		host: host,
		port: port,
		senderService: senderService{
			started: time.Now().Unix(),
		},
	}
}

type monalisaSvc struct {
	senderService
	name string
	host string
	port int
}

func (m *monalisaSvc) Name() string {
	return "monalisa"
}

func (m *monalisaSvc) send(data interface{}) error {
	return m.sendWithContext(context.TODO(), data)
}

// Send dispatches the given metric to the Monalisa service
func (m *monalisaSvc) sendWithContext(ctx context.Context, data interface{}) error {
	mondata, ok := data.(*monalisaData)
	if !ok {
		// wrong type passed in
		return fmt.Errorf(
			"Wrong data type passed to Monalisa service send() - got %T, need *monalisaData",
			data,
		)

	}

	var (
		err  error
		conn net.Conn
	)

	defer func() {
		m.updateSendCounts(err)
	}()

	addr := fmt.Sprintf("%s:%d", m.host, m.port)
	conn, err = (&net.Dialer{}).DialContext(ctx, "UDP", addr)
	defer conn.Close()

	if err != nil {
		err = monalisaError{
			err:  err,
			host: m.host,
			port: m.port,
		}
	} else {
		message := fmt.Sprintf("%s %s %s", mondata.path, mondata.name, mondata.value)
		fmt.Fprintf(conn, message)
	}

	return err
}

// ---------------------------------------------------------

type monalisaError struct {
	host string
	port int
	err  error
}

func (m monalisaError) Error() string {
	return fmt.Sprintf("%s:%d", m.host, m.port)
}

// ---------------------------------------------------------
