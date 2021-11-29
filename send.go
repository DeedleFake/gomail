package gomail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
)

// sender is the interface that wraps the Send method.
//
// Send sends an email to the given addresses.
type sender interface {
	Send(ctx context.Context, from string, to []string, msg io.WriterTo) error
}

// sendCloser is the interface that groups the Send and Close methods.
type sendCloser interface {
	sender
	Close() error
}

// A sendFunc is a function that sends emails to the given addresses.
//
// The sendFunc type is an adapter to allow the use of ordinary functions as
// email senders. If f is a function with the appropriate signature, sendFunc(f)
// is a sender object that calls f.
type sendFunc func(ctx context.Context, from string, to []string, msg io.WriterTo) error

func (f sendFunc) Send(ctx context.Context, from string, to []string, msg io.WriterTo) error {
	return f(ctx, from, to, msg)
}

// sendAll sends emails using the given sender.
func sendAll(ctx context.Context, s sender, msg ...*Message) error {
	for i, m := range msg {
		if err := send(ctx, s, m); err != nil {
			return fmt.Errorf("gomail: could not send email %d: %v", i+1, err)
		}
	}

	return nil
}

func send(ctx context.Context, s sender, m *Message) error {
	from, err := m.getFrom()
	if err != nil {
		return err
	}

	to, err := m.getRecipients()
	if err != nil {
		return err
	}

	if err := s.Send(ctx, from, to, m); err != nil {
		return err
	}

	return nil
}

func (m *Message) getFrom() (string, error) {
	from := m.header["Sender"]
	if len(from) == 0 {
		from = m.header["From"]
		if len(from) == 0 {
			return "", errors.New(`gomail: invalid message, "From" field is absent`)
		}
	}

	return parseAddress(from[0])
}

func (m *Message) getRecipients() ([]string, error) {
	n := 0
	for _, field := range []string{"To", "Cc", "Bcc"} {
		if addresses, ok := m.header[field]; ok {
			n += len(addresses)
		}
	}
	list := make([]string, 0, n)

	for _, field := range []string{"To", "Cc", "Bcc"} {
		if addresses, ok := m.header[field]; ok {
			for _, a := range addresses {
				addr, err := parseAddress(a)
				if err != nil {
					return nil, err
				}
				list = addAddress(list, addr)
			}
		}
	}

	return list, nil
}

func addAddress(list []string, addr string) []string {
	for _, a := range list {
		if addr == a {
			return list
		}
	}

	return append(list, addr)
}

func parseAddress(field string) (string, error) {
	addr, err := mail.ParseAddress(field)
	if err != nil {
		return "", fmt.Errorf("gomail: invalid address %q: %v", field, err)
	}
	return addr.Address, nil
}
