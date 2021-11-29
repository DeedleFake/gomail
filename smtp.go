package gomail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"strings"
	"sync"
)

// A Dialer is a dialer to an SMTP server.
type Dialer struct {
	// Host represents the host of the SMTP server.
	Host string

	// Port represents the port of the SMTP server.
	Port int

	// Username is the username to use to authenticate to the SMTP server.
	Username string

	// Password is the password to use to authenticate to the SMTP server.
	Password string

	// Auth represents the authentication mechanism used to authenticate to the
	// SMTP server.
	Auth smtp.Auth

	// SSL defines whether an SSL connection is used. It should be false in
	// most cases since the authentication mechanism should use the STARTTLS
	// extension instead.
	SSL bool

	// TSLConfig represents the TLS configuration used for the TLS (when the
	// STARTTLS extension is used) or SSL connection.
	TLSConfig *tls.Config

	// LocalName is the hostname sent to the SMTP server with the HELO command.
	// By default, "localhost" is sent.
	LocalName string

	// Dialer is the net.Dialer used to dial new connections to the server.
	Dialer net.Dialer
}

// NewDialer returns a new SMTP Dialer. The given parameters are used to connect
// to the SMTP server.
func NewDialer(host string, port int, username, password string) *Dialer {
	return &Dialer{
		Host:     host,
		Port:     port,
		Username: username,
		Password: password,
		SSL:      port == 465,
	}
}

// Dial dials and authenticates to an SMTP server. The returned sendCloser
// should be closed when done using it.
//
// Expiration of the provided context after the this function returns
// will have no effect.
//
// TODO: Find a way to fix context handling when sending in a way that
// allows this to be exported again.
func (d *Dialer) dial(ctx context.Context) (sc sendCloser, err error) {
	conn, err := netDialContext(&d.Dialer, ctx, "tcp", addr(d.Host, d.Port))
	if err != nil {
		return nil, err
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	if d.SSL {
		conn = tlsClient(conn, d.tlsConfig())
	}

	c, err := smtpNewClient(conn, d.Host)
	if err != nil {
		return nil, err
	}

	if d.LocalName != "" {
		if err := c.Hello(d.LocalName); err != nil {
			return nil, err
		}
	}

	if !d.SSL {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(d.tlsConfig()); err != nil {
				c.Close()
				return nil, err
			}
		}
	}

	if d.Auth == nil && d.Username != "" {
		if ok, auths := c.Extension("AUTH"); ok {
			if strings.Contains(auths, "CRAM-MD5") {
				d.Auth = smtp.CRAMMD5Auth(d.Username, d.Password)
			} else if strings.Contains(auths, "LOGIN") &&
				!strings.Contains(auths, "PLAIN") {
				d.Auth = &loginAuth{
					username: d.Username,
					password: d.Password,
					host:     d.Host,
				}
			} else {
				d.Auth = smtp.PlainAuth("", d.Username, d.Password, d.Host)
			}
		}
	}

	if d.Auth != nil {
		if err = c.Auth(d.Auth); err != nil {
			c.Close()
			return nil, err
		}
	}

	return &smtpSender{c, d, conn}, nil
}

func (d *Dialer) tlsConfig() *tls.Config {
	if d.TLSConfig == nil {
		return &tls.Config{ServerName: d.Host}
	}
	return d.TLSConfig
}

func addr(host string, port int) string {
	return fmt.Sprintf("%s:%d", host, port)
}

// DialAndSend opens a connection to the SMTP server, sends the given emails and
// closes the connection.
func (d *Dialer) DialAndSend(ctx context.Context, m ...*Message) (err error) {
	defer func() {
		if cerr := ctx.Err(); (cerr != nil) && errors.Is(err, net.ErrClosed) {
			err = cerr
		}
	}()

	s, err := d.dial(ctx)
	if err != nil {
		return err
	}
	defer s.Close()

	return sendAll(ctx, s, m...)
}

type smtpSender struct {
	smtpClient
	d    *Dialer
	conn net.Conn
}

func (c *smtpSender) Send(ctx context.Context, from string, to []string, msg io.WriterTo) (err error) {
	defer func() {
		if cerr := ctx.Err(); (cerr != nil) && errors.Is(err, net.ErrClosed) {
			err = cerr
		}
	}()

	var closer sync.Once
	done := make(chan struct{})
	defer closer.Do(func() { close(done) })
	go func() {
		select {
		case <-ctx.Done():
			c.conn.Close()
		case <-done:
		}
	}()

	if err := c.Mail(from); err != nil {
		if err == io.EOF {
			// This is probably due to a timeout, so reconnect and try again.
			sc, derr := c.d.dial(ctx)
			if derr == nil {
				if s, ok := sc.(*smtpSender); ok {
					*c = *s
					closer.Do(func() { close(done) })
					return c.Send(ctx, from, to, msg)
				}
			}
		}
		return err
	}

	for _, addr := range to {
		if err := c.Rcpt(addr); err != nil {
			return err
		}
	}

	w, err := c.Data()
	if err != nil {
		return err
	}

	if _, err = msg.WriteTo(w); err != nil {
		w.Close()
		return err
	}

	return w.Close()
}

func (c *smtpSender) Close() error {
	return c.Quit()
}

// Stubbed out for tests.
var (
	netDialContext = (*net.Dialer).DialContext
	tlsClient      = tls.Client
	smtpNewClient  = func(conn net.Conn, host string) (smtpClient, error) {
		return smtp.NewClient(conn, host)
	}
)

type smtpClient interface {
	Hello(string) error
	Extension(string) (bool, string)
	StartTLS(*tls.Config) error
	Auth(smtp.Auth) error
	Mail(string) error
	Rcpt(string) error
	Data() (io.WriteCloser, error)
	Quit() error
	Close() error
}
